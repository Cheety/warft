package workpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Cheety/warft/platform/internal/runner"
)

// Command is `workpod pod …` — the runner from the outside.
//
// Until AP-6.2 grants a lease over a real order there is no path from the queue to a pod, so this is
// how a pod is started: by hand, from a job stated by hand. That is E-11's third step read literally
// ("one adapter, one pipeline, one runner — without a captain, jobs by hand") and the same seam
// decisions/jobs-by-hand.md opened for intake.
//
//	pod run      --job FILE [--base DIR] [--reap quiet|report]   run one job to a patch and a report
//	pod resolve  --job FILE                                      T-03: the image, or the build job a miss makes
//	pod image import --skeleton DIR --requirements FILE [--layer SRC:DST] [--env K=V]
//	pod base KEY --from DIR                                      a working-copy base to snapshot from
//	pod list                                                     what is on this node
//	pod reap [--all]                                             the sweep, by hand
func Command(args []string, out io.Writer) error {
	if len(args) == 0 {
		return usage()
	}
	s := Default()
	switch args[0] {
	case "run":
		return cmdRun(s, args[1:], out)
	case "resolve":
		return cmdResolve(s, args[1:], out)
	case "image":
		if len(args) < 2 || args[1] != "import" {
			return usage()
		}
		return cmdImageImport(s, args[2:], out)
	case "base":
		return cmdBase(s, args[1:], out)
	case "list":
		return cmdList(s, out)
	case "reap":
		return cmdReap(s, args[1:], out)
	default:
		return usage()
	}
}

func usage() error {
	return fmt.Errorf("pod run | pod resolve | pod image import | pod base | pod list | pod reap")
}

// flags is a small argument reader. The platform binary takes no configuration file and no
// environment (SP-A04-4), so what a command needs it is told on the line.
type flags struct {
	single map[string]string
	repeat map[string][]string
	free   []string
}

func parseFlags(args []string, repeatable ...string) (flags, error) {
	f := flags{single: map[string]string{}, repeat: map[string][]string{}}
	rep := map[string]bool{}
	for _, r := range repeatable {
		rep[r] = true
	}
	for i := 0; i < len(args); i++ {
		name, ok := strings.CutPrefix(args[i], "--")
		if !ok {
			f.free = append(f.free, args[i])
			continue
		}
		i++
		if i >= len(args) {
			return f, fmt.Errorf("--%s wants a value", name)
		}
		if rep[name] {
			f.repeat[name] = append(f.repeat[name], args[i])
		} else {
			f.single[name] = args[i]
		}
	}
	return f, nil
}

func readJobFile(path string) (runner.Job, error) {
	var job runner.Job
	if path == "" {
		return job, fmt.Errorf("--job names the file the job stands in (decisions/jobs-by-hand.md)")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return job, err
	}
	return job, json.Unmarshal(b, &job)
}

func cmdRun(s Store, args []string, out io.Writer) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	job, err := readJobFile(f.single["job"])
	if err != nil {
		return err
	}
	w := New(s)
	w.Log = func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }
	if r := f.single["reap"]; r != "" {
		switch ReapAfter(r) {
		case AfterQuiet, AfterReport:
			w.Reap = ReapAfter(r)
		default:
			return fmt.Errorf("--reap is %s or %s", AfterQuiet, AfterReport)
		}
	}

	rep, runErr := w.Run(context.Background(), f.single["base"], job)
	// The report is printed even when the run failed. A pod that ended badly is exactly the pod
	// whose lifecycle and cause someone needs to read (SP-K02-3).
	body, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", body)
	return runErr
}

func cmdResolve(s Store, args []string, out io.Writer) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	job, err := readJobFile(f.single["job"])
	if err != nil {
		return err
	}
	m, err := s.Resolve(job)
	if err != nil {
		var miss *Miss
		if errors.As(err, &miss) {
			fmt.Fprintf(out, "miss\t%s\nbuild-job\t%s\n", miss.RequirementHash, miss.BuildJobPath)
		}
		return err
	}
	fmt.Fprintf(out, "hit\t%s\nimage\t%s\n", job.Requirements.Hash(), m.Digest())
	return nil
}

func cmdImageImport(s Store, args []string, out io.Writer) error {
	f, err := parseFlags(args, "layer", "env")
	if err != nil {
		return err
	}
	skeleton := f.single["skeleton"]
	if skeleton == "" {
		return fmt.Errorf("--skeleton names the directory of mount points a pod's root is laid down from")
	}
	var req runner.Requirements
	if p := f.single["requirements"]; p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &req); err != nil {
			return err
		}
	}
	var layers []Layer
	for _, spec := range f.repeat["layer"] {
		src, dst, ok := strings.Cut(spec, ":")
		if !ok {
			return fmt.Errorf("--layer takes SOURCE:DESTINATION, not %q", spec)
		}
		layers = append(layers, Layer{Source: src, Destination: dst})
	}
	m, err := s.Import(skeleton, layers, f.repeat["env"], req)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "image\t%s\nrequirements\t%s\nskeleton\t%s\n", m.Digest(), m.RequirementHash, m.Skeleton)
	return nil
}

func cmdBase(s Store, args []string, out io.Writer) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(f.free) != 1 {
		return fmt.Errorf("pod base KEY --from DIR")
	}
	path, err := s.EnsureBase(f.free[0], f.single["from"])
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "base\t%s\n", path)
	return nil
}

func cmdList(s Store, out io.Writer) error {
	pods, err := s.Pods()
	if err != nil {
		return err
	}
	known, err := containers()
	if err != nil {
		known = map[string]runcState{}
	}
	r := &Reaper{Pod: New(s)}
	ids := map[string]bool{}
	for _, id := range pods {
		ids[id] = true
	}
	for id := range known {
		ids[id] = true
	}
	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		state := known[id].Status
		if state == "" {
			state = "no-container"
		}
		watched := "orphan"
		if r.supervised(id) {
			watched = "supervised"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\n", id, state, watched)
	}
	return nil
}

func cmdReap(s Store, args []string, out io.Writer) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	r := &Reaper{Pod: New(s), Log: func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }}
	if len(f.free) == 1 {
		return r.Pod.reap(f.free[0], nil)
	}
	reaped, err := r.Sweep()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "reaped\t%d\n", len(reaped))
	return nil
}
