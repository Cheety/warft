-- schema.sql — the state contract of the cell (K-01, K-02, E-02).
--
-- Principles enforced here and not left to the application:
--   1. cell and project are NOT NULL on every table (SP-K01-3, SP-K01-4). A table stands exempt
--      only where another MUST of the specification forces it — the closed list, each entry with
--      the requirement that forces it, lives in acceptance/schema-additive.py.
--   2. Identifiers come from the producer as UUID v7, never from a sequence (SP-K01-2).
--   3. One field, one writer: state transitions are checked by a trigger (SP-K02-1).
--   4. No terminal state without a cause (SP-K02-3).
--   5. No backward transitions (SP-K02-5).
--   6. No secrets, only references (SP-K01-5).
--   7. The queue lives in this database, no second broker (SP-E02-2).
--   8. The lease and heartbeat parameters are ruled once (OP-4) and seeded as rows, never carried
--      as numbers in application code.
--
-- Migrations run additively, exclusively (SP-V05-2): write the new field, read both, remove the
-- old one — three releases. acceptance/schema-additive.py holds every revision to that rule
-- against its predecessor, and acceptance/k01-schema.sh probes that the rejection bites.

BEGIN;

-- ---------------------------------------------------------------------------
-- Enumerations (names taken from the design unchanged)
-- ---------------------------------------------------------------------------

CREATE TYPE authority_level AS ENUM ('public', 'linked', 'confidential');
CREATE TYPE resource_class  AS ENUM ('tiny', 'small', 'medium', 'large');
CREATE TYPE priority        AS ENUM ('interactive', 'batch', 'maintenance', 'background');
CREATE TYPE order_state     AS ENUM ('new','admitted','queued','leased','running','frozen',
                                     'awaiting_reply','delivered','unproven','failed','cancelled');
CREATE TYPE evidence_class  AS ENUM ('artifact.identical','types.lint','tests.existing','tests.new',
                                     'mutation.diff','review.independent','human');
CREATE TYPE cause_code      AS ENUM ('fact.invented','context.missing','spec.wrong','tool.failure',
                                     'injection','regression.silent','assumption.replicated',
                                     'skill.missing','knowledge.missing','goal.wrong',
                                     'budget.exhausted','unsolvable');
CREATE TYPE reversibility   AS ENUM ('reversible','costly','irreversible');
CREATE TYPE writer_role     AS ENUM ('control','worker');

-- ---------------------------------------------------------------------------
-- Master data
-- ---------------------------------------------------------------------------

CREATE TABLE cell (
  id            text PRIMARY KEY,          -- 'eu-c1'
  region        text NOT NULL DEFAULT 'eu',-- E-07: one region per tenant, default the EU
  tenant        text NOT NULL,             -- E-03: the tenant cuts the cell
  retention     jsonb NOT NULL,            -- E-07: a cell property, not a runtime setting
  active_project_limit int NOT NULL DEFAULT 500  -- E-04
);

CREATE TABLE principal (
  id       uuid PRIMARY KEY,
  cell     text NOT NULL REFERENCES cell(id),
  daily_money_cap_micros bigint NOT NULL   -- V-04: only a human raises it (E-08: two people)
);

CREATE TABLE identity_link (               -- T-01: never attribute automatically
  external_id   text PRIMARY KEY,          -- 'discord:184…'
  principal     uuid NOT NULL REFERENCES principal(id),
  cell          text NOT NULL REFERENCES cell(id),
  confirmed_via text NOT NULL,             -- the channel of confirmation, must be a different one
  confirmed_at  timestamptz NOT NULL
);

CREATE TABLE project (
  id        uuid PRIMARY KEY,
  cell      text NOT NULL REFERENCES cell(id),   -- V-03: a project lies wholly in one cell
  principal uuid NOT NULL REFERENCES principal(id),
  model_provider_pin text,                       -- E-06: the provider pinned per project
  prompt_version_pin text,                       -- Q-04: a project does not change generation
  frozen    boolean NOT NULL DEFAULT false       -- B-04: first remedy on suspected injection
);

-- ---------------------------------------------------------------------------
-- K-01: the three objects
-- ---------------------------------------------------------------------------

CREATE TABLE envelope (
  id                 uuid PRIMARY KEY,
  cell               text NOT NULL REFERENCES cell(id),
  project            uuid NOT NULL REFERENCES project(id),
  channel            text NOT NULL,
  channel_message_id text NOT NULL,
  sender_external    text NOT NULL,
  principal          uuid REFERENCES principal(id),   -- empty = first contact, produces an invitation
  authority          authority_level NOT NULL,        -- from the channel, not from the text
  text_body          text NOT NULL,
  attachments        text[] NOT NULL DEFAULT '{}',    -- content hashes into attachment (SP-K01-6):
                                                      -- references, resolved at intake, never payloads
  thread             text,
  received_at        timestamptz NOT NULL,
  idempotency        text NOT NULL,
  platform           text NOT NULL DEFAULT 'alpine',
  purge_after        timestamptz NOT NULL,            -- E-07: 30 days, the reference remains
  UNIQUE (channel, idempotency)                       -- SP-T01-7: one retry, one job
);

CREATE TABLE attachment (
  content_hash text PRIMARY KEY,
  cell         text NOT NULL REFERENCES cell(id),
  project      uuid NOT NULL REFERENCES project(id),
  media_type   text NOT NULL,
  size_bytes   bigint NOT NULL,
  executable   boolean NOT NULL DEFAULT false CHECK (executable = false)  -- never executable
);

CREATE TABLE spec (
  id          uuid NOT NULL,
  version     bigint NOT NULL,
  cell        text NOT NULL REFERENCES cell(id),
  project     uuid NOT NULL REFERENCES project(id),
  envelope_id uuid NOT NULL REFERENCES envelope(id),   -- provenance chain
  goal        text NOT NULL,
  bounds      jsonb NOT NULL,
  budget      jsonb NOT NULL,
  risk_class  reversibility NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, version)                            -- versions are never overwritten
);

CREATE TABLE acceptance (                              -- without a row here, no job (SP-Q01-6)
  id           uuid PRIMARY KEY,
  cell         text NOT NULL REFERENCES cell(id),
  project      uuid NOT NULL REFERENCES project(id),
  spec_id      uuid NOT NULL,
  spec_version bigint NOT NULL,
  statement    text NOT NULL,
  required_evidence evidence_class NOT NULL,
  machine_checkable boolean NOT NULL,
  FOREIGN KEY (spec_id, spec_version) REFERENCES spec(id, version)
);

CREATE TABLE assumption (                              -- objects, not prose (SP-Q01-5)
  id           uuid PRIMARY KEY,
  cell         text NOT NULL REFERENCES cell(id),
  project      uuid NOT NULL REFERENCES project(id),
  spec_id      uuid NOT NULL,
  spec_version bigint NOT NULL,
  statement    text NOT NULL,
  revoked      boolean NOT NULL DEFAULT false,
  revoked_reason text,
  FOREIGN KEY (spec_id, spec_version) REFERENCES spec(id, version)
);

CREATE TABLE "order" (
  id             uuid PRIMARY KEY,
  cell           text NOT NULL REFERENCES cell(id),
  project        uuid NOT NULL REFERENCES project(id),
  spec_id        uuid NOT NULL,
  spec_version   bigint NOT NULL,
  parent         uuid REFERENCES "order"(id),
  class          resource_class NOT NULL,
  platform       text NOT NULL,
  image_hash     text NOT NULL,
  pipeline_version text NOT NULL,
  authority_ref  text NOT NULL,          -- a reference to the Biscuit token, never the secret itself
  budget_share   jsonb NOT NULL,
  locality_group text NOT NULL,
  state          order_state NOT NULL DEFAULT 'new',
  attempt        int NOT NULL DEFAULT 1,
  idempotency    text NOT NULL,
  priority       priority NOT NULL DEFAULT 'batch',
  model_version  text,                   -- Q-04: belongs in the job log
  prompt_version text,
  cause          cause_code,
  evidence       evidence_class,
  -- V-04: what the job actually spent, against what it reserved at admission. The worker writes
  -- these with its report, before it writes the terminal state; the release then hands back the
  -- difference (release_reservation below). Zero means "nothing measured yet", which is the honest
  -- state of a job that has not run.
  spent_pod_minutes  bigint NOT NULL DEFAULT 0,
  spent_tokens       bigint NOT NULL DEFAULT 0,
  spent_money_micros bigint NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (spec_id, spec_version) REFERENCES spec(id, version),
  -- SP-K02-3: no terminal state without a cause
  CONSTRAINT end_state_needs_cause CHECK (
    state NOT IN ('failed','unproven','cancelled') OR cause IS NOT NULL
  ),
  -- Q-02: delivered only with an evidence class
  CONSTRAINT delivered_needs_evidence CHECK (
    state <> 'delivered' OR evidence IS NOT NULL
  ),
  UNIQUE (project, idempotency, attempt)
);

CREATE INDEX order_queue_idx ON "order" (cell, locality_group, priority, created_at)
  WHERE state = 'queued';

-- ---------------------------------------------------------------------------
-- K-02: one field, one writer — enforced, not agreed
-- ---------------------------------------------------------------------------

CREATE TABLE state_transition (
  from_state order_state NOT NULL,
  to_state   order_state NOT NULL,
  writer     writer_role NOT NULL,
  PRIMARY KEY (from_state, to_state)
);

INSERT INTO state_transition (from_state, to_state, writer) VALUES
  ('new','admitted','control'),
  ('admitted','queued','control'),
  ('queued','leased','control'),          -- the worker requests, the control plane writes
  ('leased','queued','control'),          -- deadline expired, no heartbeat
  ('leased','running','worker'),
  ('running','frozen','worker'),          -- preemption at the phase boundary
  ('frozen','running','worker'),
  ('running','awaiting_reply','worker'),
  ('awaiting_reply','running','worker'),
  ('running','delivered','worker'),
  ('running','unproven','worker'),
  ('running','failed','worker'),
  ('new','cancelled','control'),
  ('admitted','cancelled','control'),
  ('queued','cancelled','control'),
  ('leased','cancelled','control'),
  ('running','cancelled','control'),
  ('frozen','cancelled','control'),
  ('awaiting_reply','cancelled','control');

-- Before every update the application sets: SET LOCAL workpod.writer = 'control' | 'worker'.
CREATE OR REPLACE FUNCTION enforce_transition() RETURNS trigger AS $$
DECLARE
  allowed_writer writer_role;
  claimed_writer text := current_setting('workpod.writer', true);
BEGIN
  IF NEW.state = OLD.state THEN
    RETURN NEW;
  END IF;

  -- SP-K02-5: no way back out of a terminal state
  IF OLD.state IN ('delivered','unproven','failed','cancelled') THEN
    RAISE EXCEPTION 'K-02: terminal state % cannot be left', OLD.state;
  END IF;

  SELECT writer INTO allowed_writer
    FROM state_transition
   WHERE from_state = OLD.state AND to_state = NEW.state;

  IF allowed_writer IS NULL THEN
    RAISE EXCEPTION 'K-02: transition % -> % does not exist', OLD.state, NEW.state;
  END IF;

  IF claimed_writer IS NULL OR claimed_writer <> allowed_writer::text THEN
    RAISE EXCEPTION 'K-02: % -> % is written exclusively by %, not by %',
      OLD.state, NEW.state, allowed_writer, coalesce(claimed_writer,'<not set>');
  END IF;

  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER order_transition
  BEFORE UPDATE ON "order"
  FOR EACH ROW EXECUTE FUNCTION enforce_transition();

-- K-02: the attempt is the unit of retry — same order id, same idempotency key, new attempt
-- counter, its own result (SP-K02-2). SP-K01-8 makes it an object of its own. The result columns
-- live per attempt so a retry's verdict never overwrites its predecessor's; the order carries only
-- the verdict of the attempt that ended it.
CREATE TABLE attempt (
  order_id   uuid NOT NULL REFERENCES "order"(id),
  attempt    int NOT NULL,
  cell       text NOT NULL REFERENCES cell(id),
  project    uuid NOT NULL REFERENCES project(id),
  started_at timestamptz NOT NULL DEFAULT now(),
  ended_at   timestamptz,
  cause      cause_code,                   -- its own result: how this attempt ended, if badly
  evidence   evidence_class,               -- or the evidence class it delivered (Q-02)
  PRIMARY KEY (order_id, attempt)
);

-- ---------------------------------------------------------------------------
-- V-02: leases. The queue with SKIP LOCKED, no second broker.
-- ---------------------------------------------------------------------------

CREATE TABLE node (
  id             text PRIMARY KEY,        -- role and cell stand in the certificate name (B-01)
  cell           text NOT NULL REFERENCES cell(id),
  role           text NOT NULL CHECK (role IN ('all','control','knowledge','work')),
  locality_group text,
  image_version  bigint NOT NULL,         -- A-03: a number, not a timestamp
  channel        text NOT NULL CHECK (channel IN ('canary','stable','held')),
  healthy_since  timestamptz,             -- healthy only after a completed job (SP-A03-4)
  cert_expires   timestamptz NOT NULL
);

CREATE TABLE lease (
  order_id   uuid NOT NULL REFERENCES "order"(id),
  attempt    int NOT NULL,
  cell       text NOT NULL REFERENCES cell(id),
  project    uuid NOT NULL REFERENCES project(id),
  node_id    text NOT NULL REFERENCES node(id),
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (order_id, attempt),
  -- a lease is granted on an attempt that exists (SP-K02-2); granting one is AP-6.2's work
  FOREIGN KEY (order_id, attempt) REFERENCES attempt (order_id, attempt)
);

-- OP-4 (decisions/OP-4.md): lease 60 s, heartbeat 15 s, three missed heartbeats release. The
-- parameters of SP-V02-1's pull model stand here as rows the control plane reads, not as numbers
-- in application code. This seed is the machine-readable half of the ruling, and
-- acceptance/k02-state.sh holds the two against each other — a number there that is not here is
-- drift.
CREATE TABLE lease_parameter (
  name  text PRIMARY KEY,
  value int NOT NULL CHECK (value > 0)
);

INSERT INTO lease_parameter (name, value) VALUES
  ('lease_duration_seconds',     60),   -- the deadline a job is handed out with (SP-V02-1)
  ('heartbeat_interval_seconds', 15),   -- the worker extends its lease this often
  ('failures_to_release',         3);   -- missed heartbeats in a row before leased -> queued

-- V-02: sticky assignment repository -> node, queues and work stealing inside the group (OP-8).
CREATE TABLE locality_group (
  id   text NOT NULL,                      -- 'monorepo-a'
  cell text NOT NULL REFERENCES cell(id),
  PRIMARY KEY (cell, id)
);

-- Allocation (control plane):
--   SELECT id FROM "order"
--    WHERE state='queued' AND cell=$1 AND locality_group=$2
--    ORDER BY priority, created_at
--    FOR UPDATE SKIP LOCKED LIMIT $3;
--
-- The ORDER BY the scheduler actually issues carries decisions/aging.md's three keys in front of
-- the priority column — overdue first, then the largest wait/bound ratio — and it is generated from
-- SP-RB-2's bounds in `internal/statedb`, never written out here. The shape above is the one that
-- matters to the state contract: one table, SKIP LOCKED, no second broker (SP-RB-7, SP-E02-2).

-- ---------------------------------------------------------------------------
-- R-C: predict instead of clean up (SP-RC-6)
-- ---------------------------------------------------------------------------

-- Peak RSS and runtime per repository and phase. After three runs admission decides from these rows
-- mechanically; below three it decides on pressure alone, because two measurements of a phase are
-- two anecdotes.
--
-- The repository is the order's `locality_group`: OP-8 makes that group the sticky assignment
-- repository -> node, so it is the state contract's name for what SP-RC-6 calls a repository, and a
-- second column holding the same string would be a second thing to keep true.
--
-- Both aggregates are maxima rather than averages. Admission asks whether a job fits, and a mean
-- answers a different question — half the runs of a repository whose mean fits do not fit. This is
-- also why the five constants of E-05 are not here: they are planning values for the occupancy
-- table (SP-RD-3), and these are measurements.
CREATE TABLE phase_profile (
  cell           text NOT NULL REFERENCES cell(id),
  project        uuid NOT NULL REFERENCES project(id),
  repository     text NOT NULL,
  phase          text NOT NULL,        -- one of T-05's seven (SP-T05-1); the spine is the program's
  runs           int NOT NULL DEFAULT 0 CHECK (runs >= 0),
  peak_rss_bytes bigint NOT NULL DEFAULT 0 CHECK (peak_rss_bytes >= 0),
  runtime_ms     bigint NOT NULL DEFAULT 0 CHECK (runtime_ms >= 0),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (cell, project, repository, phase)
);

-- ---------------------------------------------------------------------------
-- K-03: outbox and receipts
-- ---------------------------------------------------------------------------

CREATE TABLE outbox (
  order_id     uuid NOT NULL REFERENCES "order"(id),
  target       text NOT NULL,
  content_hash text NOT NULL,
  cell         text NOT NULL REFERENCES cell(id),
  project      uuid NOT NULL REFERENCES project(id),
  payload_ref  text NOT NULL,
  requires_register boolean NOT NULL DEFAULT false,
  registered_at timestamptz,
  executed_at   timestamptz,
  receipt_id    text,
  PRIMARY KEY (order_id, target, content_hash)   -- SP-K03-2: the key is a domain key
);

-- SP-K03-1: the receipt comes back from the gate that executed the entry; outbox.receipt_id names
-- it. For non-idempotent targets it is the register's acknowledgement (SP-K03-4).
CREATE TABLE receipt (
  id           uuid PRIMARY KEY,
  cell         text NOT NULL REFERENCES cell(id),
  project      uuid NOT NULL REFERENCES project(id),
  order_id     uuid NOT NULL,
  target       text NOT NULL,
  content_hash text NOT NULL,
  issued_by    text NOT NULL,               -- which gate let it through (B-02: there are two)
  issued_at    timestamptz NOT NULL,
  FOREIGN KEY (order_id, target, content_hash) REFERENCES outbox (order_id, target, content_hash)
);

-- ---------------------------------------------------------------------------
-- V-04: pots. Reserve in advance, do not count afterwards.
-- ---------------------------------------------------------------------------

CREATE TABLE budget_pot (
  id         uuid PRIMARY KEY,
  cell       text NOT NULL REFERENCES cell(id),
  project    uuid REFERENCES project(id),
  principal  uuid REFERENCES principal(id),
  scope      text NOT NULL CHECK (scope IN ('envelope','project','principal_day',
                                            'principal_channel_day')),
  -- OP-1: the caps are ruled per authority level, so a pot is keyed by one. A stranger in an open
  -- channel cannot spend the day a confidential channel was granted, even when both belong to the
  -- same principal, because the two draw from different pots.
  authority  authority_level NOT NULL DEFAULT 'public',
  envelope   uuid REFERENCES envelope(id),   -- the envelope scope, SP-V04-5: against abuse
  channel    text,                           -- SP-T01-8: pod minutes per principal and channel
  day        date,                           -- the two daily scopes; UTC, the cell's own day
  pod_minutes_reserved bigint NOT NULL DEFAULT 0,
  pod_minutes_cap      bigint NOT NULL,
  tokens_reserved      bigint NOT NULL DEFAULT 0,
  tokens_cap           bigint NOT NULL,
  money_reserved_micros bigint NOT NULL DEFAULT 0,
  money_cap_micros      bigint NOT NULL,
  -- SP-V04-5: three caps, three purposes; SP-T01-8 adds the fourth. The two daily scopes are the
  -- ones that span projects, and the only lawful reason for an empty project on this table.
  CONSTRAINT pot_scope_project   CHECK ((scope IN ('principal_day','principal_channel_day'))
                                        = (project IS NULL)),
  CONSTRAINT pot_scope_principal CHECK (scope NOT IN ('principal_day','principal_channel_day')
                                        OR (principal IS NOT NULL AND day IS NOT NULL)),
  CONSTRAINT pot_scope_envelope  CHECK ((scope = 'envelope') = (envelope IS NOT NULL)),
  CONSTRAINT pot_scope_channel   CHECK ((scope = 'principal_channel_day') = (channel IS NOT NULL)),
  CHECK (pod_minutes_reserved <= pod_minutes_cap),
  CHECK (tokens_reserved <= tokens_cap),
  CHECK (money_reserved_micros <= money_cap_micros)
);

-- One pot per key, or the reservation would be spread over pots nobody can find again. The keys
-- are what OP-1 rules the caps by: the scope, the authority level, and what the scope is about.
CREATE UNIQUE INDEX budget_pot_envelope_idx ON budget_pot (cell, envelope, authority)
  WHERE scope = 'envelope';
CREATE UNIQUE INDEX budget_pot_project_idx ON budget_pot (cell, project, authority)
  WHERE scope = 'project';
CREATE UNIQUE INDEX budget_pot_principal_day_idx ON budget_pot (cell, principal, day, authority)
  WHERE scope = 'principal_day';
CREATE UNIQUE INDEX budget_pot_channel_day_idx ON budget_pot (cell, principal, channel, day, authority)
  WHERE scope = 'principal_channel_day';

-- SP-V04-3: reserve in advance, do not count afterwards — and release what was not spent at the
-- terminal state. This table is what makes the second half possible: it records which pots an
-- admitted job holds and how much of each, so the release is arithmetic rather than a second guess
-- at what the job was going to cost.
CREATE TABLE budget_reservation (
  order_id    uuid NOT NULL REFERENCES "order"(id),
  pot         uuid NOT NULL REFERENCES budget_pot(id),
  cell        text NOT NULL REFERENCES cell(id),
  project     uuid NOT NULL REFERENCES project(id),
  pod_minutes  bigint NOT NULL,
  tokens       bigint NOT NULL,
  money_micros bigint NOT NULL,
  reserved_at timestamptz NOT NULL DEFAULT now(),
  released_at timestamptz,
  PRIMARY KEY (order_id, pot)
);

-- V-04: the release at the terminal state is a rule of the contract, not a courtesy of whoever
-- writes the state. The worker writes `delivered`, `unproven` and `failed` (SP-K02-1) and the
-- control plane writes `cancelled`; neither of them should have to remember the pots, and a job
-- whose process died between the two writes would otherwise hold its reservation for ever.
--
-- What is released is the unspent part: the reservation minus what the job actually spent, which
-- the worker records on the order before it ends it. A job that spent more than it reserved
-- releases nothing — the overspend stays reserved until a human looks at it.
CREATE OR REPLACE FUNCTION release_reservation() RETURNS trigger AS $$
BEGIN
  IF NEW.state IN ('delivered','unproven','failed','cancelled') AND NEW.state <> OLD.state THEN
    UPDATE budget_pot p SET
      pod_minutes_reserved  = p.pod_minutes_reserved
                              - greatest(r.pod_minutes  - least(NEW.spent_pod_minutes,  r.pod_minutes),  0),
      tokens_reserved       = p.tokens_reserved
                              - greatest(r.tokens       - least(NEW.spent_tokens,       r.tokens),       0),
      money_reserved_micros = p.money_reserved_micros
                              - greatest(r.money_micros - least(NEW.spent_money_micros, r.money_micros), 0)
      FROM budget_reservation r
     WHERE r.pot = p.id AND r.order_id = NEW.id AND r.released_at IS NULL;

    UPDATE budget_reservation SET released_at = now()
     WHERE order_id = NEW.id AND released_at IS NULL;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER order_release_budget
  AFTER UPDATE ON "order"
  FOR EACH ROW EXECUTE FUNCTION release_reservation();

-- ---------------------------------------------------------------------------
-- The versioned artifacts a job pins (SP-K01-8): capability, container image,
-- pipeline. The cell serves them; a project pins them by hash or version.
-- ---------------------------------------------------------------------------

CREATE TABLE skill_version (                -- F-07: capability versions are content-addressed
  content_hash text PRIMARY KEY,
  cell         text NOT NULL REFERENCES cell(id),
  name         text NOT NULL,
  version      text NOT NULL,
  UNIQUE (cell, name, version)
);

CREATE TABLE container_image (              -- image@hash: T-03's container image an order runs in,
  hash text PRIMARY KEY,                    -- not A-03's system image
  cell text NOT NULL REFERENCES cell(id)
);

CREATE TABLE pipeline_version (             -- T-05: the fixed spine, versioned
  cell         text NOT NULL REFERENCES cell(id),
  version      text NOT NULL,
  content_hash text NOT NULL,
  PRIMARY KEY (cell, version)
);

-- ---------------------------------------------------------------------------
-- G-01: judgements as an append-only log. Facts lie as Parquet, not here (E-09).
-- ---------------------------------------------------------------------------

CREATE TABLE judgment (
  id            uuid PRIMARY KEY,
  cell          text NOT NULL REFERENCES cell(id),
  project       uuid NOT NULL REFERENCES project(id),
  statement     text NOT NULL,
  cited_facts   text[] NOT NULL,           -- content hashes; if they change, the judgement expires
  model_version text NOT NULL,
  prompt_version text NOT NULL,
  replaces      uuid REFERENCES judgment(id),   -- new ones replace old ones explicitly
  contradicts   uuid REFERENCES judgment(id),   -- contradictions are marked, not resolved
  created_at    timestamptz NOT NULL DEFAULT now()
);
-- Append-only: no UPDATE, no DELETE.
CREATE RULE judgment_no_update AS ON UPDATE TO judgment DO INSTEAD NOTHING;
CREATE RULE judgment_no_delete AS ON DELETE TO judgment DO INSTEAD NOTHING;

CREATE TABLE decision_ref (                 -- V-05: decisions lie in Git, not here
  id     uuid PRIMARY KEY,
  cell   text NOT NULL REFERENCES cell(id),
  repo   text NOT NULL,
  path   text NOT NULL,
  commit text NOT NULL
);

-- ---------------------------------------------------------------------------
-- B-03: audit trail — separate, immutable, its own period per tenant
-- ---------------------------------------------------------------------------

CREATE TABLE audit (
  id         uuid PRIMARY KEY,
  cell       text NOT NULL REFERENCES cell(id),
  project    uuid,               -- platform actions (halt.set, authority.issued) have no project;
                                 -- entries about a job carry its project
  at         timestamptz NOT NULL DEFAULT now(),
  actor      text NOT NULL,
  action     text NOT NULL,      -- authority.issued · gate.passed · human.accepted · halt.set …
  subject    text NOT NULL,
  detail     jsonb NOT NULL,
  retain_until timestamptz NOT NULL
);
CREATE RULE audit_no_update AS ON UPDATE TO audit DO INSTEAD NOTHING;
CREATE RULE audit_no_delete AS ON DELETE TO audit DO INSTEAD NOTHING;

-- ---------------------------------------------------------------------------
-- E-08: halt as a state with an expiry, not a command with an effect
-- ---------------------------------------------------------------------------

CREATE TABLE halt (
  cell       text PRIMARY KEY REFERENCES cell(id),
  reason     text NOT NULL,
  set_by     text NOT NULL,
  set_at     timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL          -- 60 minutes; not renewed means expired
);
-- Second path (SP-E08-3): /var/lib/workpod/halt on the control node, read at every admission.

COMMIT;
