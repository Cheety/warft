#!/usr/bin/env bash
# b01-identity.sh — role and cell in the certificate's name, checked as a statement (AP-2.5).
#
# One row rests on this script:
#   AB-B01-3  S  role and cell in the name — the control plane checks "work node from cell B" as a
#                statement, and rejects a node whose certified name does not match its claim
#
# The verifier below is the verification path contract/identity.md fixes for both ends of every
# gRPC connection: chain to the cell's node CA, exactly one workpod URI SAN, then the certified
# name replaces every claim made beside the channel. It runs on openssl alone, because the claim
# under test is about certificates and not about any application: there is no table in the loop,
# and each forbidden case names the refusal it expects, so a refusal for the wrong reason cannot
# pass as the right one (the k02-state.sh discipline). Controls prove the lawful node passes, so a
# verifier that refuses everything cannot pass as one that enforces the rule.
#
# The mTLS handshake is exercised in both directions with openssl s_server/s_client — the
# scaffolding half of AP-2.5: the listening end demands and verifies a client certificate, the
# dialing end verifies the listener, and both refusals happen in the TLS layer, before any
# application speaks.
#
# Every key and CA is ephemeral, minted in a scratch directory for this run; none is committed
# (decisions/signing-key.md discipline). The stage 2 boundary holds: this is the format and the
# verification path, not a server — enrollment is AP-6.1, the real listeners are stage 3.
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

SCRATCH="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

PASS=0; FAIL=0; SKIP=0

pass() { printf '  \033[32mPASS\033[0m  %-56s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-56s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m  %-56s %s\n' "$1" "${2:-}"; SKIP=$((SKIP+1)); }
banner() { printf '\n\033[1m%s\033[0m\n' "$1"; }

if ! command -v openssl >/dev/null 2>&1; then
  banner "B-01 — the name as a statement"
  skip "B01 all checks" "no openssl on this machine"
  banner "Result"
  printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
  exit 0
fi

# --------------------------------------------------------------------------
# Ephemeral material: a node CA per cell, and the certificates the cases need.
# P-256 and the extension profile come from contract/identity.md.
# --------------------------------------------------------------------------
mint_ca() { # name cn
  openssl ecparam -name prime256v1 -genkey -noout -out "$SCRATCH/$1.key" 2>/dev/null &&
  openssl req -x509 -new -key "$SCRATCH/$1.key" -sha256 -days 1 \
    -subj "/CN=$2" -out "$SCRATCH/$1.pem" 2>/dev/null
}

mint_node() { # name ca cn san (san empty = no subjectAltName at all)
  local name="$1" ca="$2" cn="$3" san="$4"
  openssl ecparam -name prime256v1 -genkey -noout -out "$SCRATCH/$name.key" 2>/dev/null &&
  openssl req -new -key "$SCRATCH/$name.key" -subj "/CN=$cn" -out "$SCRATCH/$name.csr" 2>/dev/null &&
  {
    echo "basicConstraints=critical,CA:FALSE"
    echo "keyUsage=critical,digitalSignature"
    echo "extendedKeyUsage=serverAuth,clientAuth"
    [ -n "$san" ] && echo "subjectAltName=$san"
    true
  } > "$SCRATCH/$name.ext" &&
  openssl x509 -req -in "$SCRATCH/$name.csr" -CA "$SCRATCH/$ca.pem" -CAkey "$SCRATCH/$ca.key" \
    -CAcreateserial -days 1 -sha256 -extfile "$SCRATCH/$name.ext" \
    -out "$SCRATCH/$name.pem" 2>/dev/null
}

mint_ca ca-eu-c1 "workpod node CA eu-c1" &&
mint_ca ca-eu-c2 "workpod node CA eu-c2" &&
mint_ca ca-attacker "not any cell's CA" &&
mint_node n01      ca-eu-c1    n-01 "URI:workpod://eu-c1/work/n-01" &&
mint_node n00      ca-eu-c1    n-00 "URI:workpod://eu-c1/control/n-00" &&
mint_node n02      ca-eu-c2    n-02 "URI:workpod://eu-c2/work/n-02" &&
mint_node imposter ca-attacker n-66 "URI:workpod://eu-c1/work/n-66" &&
mint_node rogue    ca-attacker n-00 "URI:workpod://eu-c1/control/n-00" &&
mint_node nameless ca-eu-c1    n-03 "DNS:n-03.internal" &&
mint_node twin     ca-eu-c1    n-04 "URI:workpod://eu-c1/work/n-04,URI:workpod://eu-c1/control/n-04" &&
mint_node oddrole  ca-eu-c1    n-05 "URI:workpod://eu-c1/gateway/n-05" || {
  fail "B01-0 ephemeral material minted" "openssl could not build the fixtures"
  banner "Result"; printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"; exit 1
}

# --------------------------------------------------------------------------
# The verification path from contract/identity.md, steps 1–3. Prints the refusal cause — or, on
# success, the statement — on stdout. This is the path the control plane will run on a peer
# certificate; the probes drive it with claims that match and claims that lie.
# --------------------------------------------------------------------------
verify_peer() { # cert-name ca-name claim_role claim_cell claim_node
  local cert="$SCRATCH/$1.pem" ca="$SCRATCH/$2.pem"
  local claim_role="$3" claim_cell="$4" claim_node="$5"

  # 1 — chain. The statement is the CA's signature over the name, not the text of the name.
  if ! openssl verify -CAfile "$ca" "$cert" >/dev/null 2>&1; then
    echo "untrusted"; return 1
  fi

  # 2 — exactly one well-formed workpod URI SAN. Ambiguity is refusal, not choice.
  local uris
  uris="$(openssl x509 -in "$cert" -noout -ext subjectAltName 2>/dev/null \
          | tr ',' '\n' | sed 's/^[[:space:]]*//' | grep '^URI:workpod://' || true)"
  if [ "$(printf '%s' "$uris" | grep -c .)" -ne 1 ]; then
    echo "no_identity"; return 1
  fi
  local name="${uris#URI:workpod://}"
  if ! printf '%s' "$name" | grep -Eq '^[a-z0-9-]+/(all|control|knowledge|work)/[a-z0-9-]+$'; then
    echo "no_identity"; return 1
  fi

  # 3 — the statement replaces the claim. The certified name wins; a claim beside the channel
  # that contradicts it is refused, never reconciled.
  local cell role node rest
  cell="${name%%/*}"; rest="${name#*/}"; role="${rest%%/*}"; node="${rest#*/}"
  [ "$cell" = "$claim_cell" ] || { echo "cell_mismatch"; return 1; }
  [ "$role" = "$claim_role" ] || { echo "role_mismatch"; return 1; }
  [ "$node" = "$claim_node" ] || { echo "node_mismatch"; return 1; }
  echo "$role node from cell $cell ($node)"
  return 0
}

allowed() { # label cert ca role cell node
  local label="$1"; shift
  local out rc
  out="$(verify_peer "$@")"; rc=$?
  if [ $rc -eq 0 ]; then pass "$label" "$out"; else fail "$label" "refused '$out'"; fi
}

refused() { # label expected_cause cert ca role cell node
  local label="$1" want="$2"; shift 2
  local out rc
  out="$(verify_peer "$@")"; rc=$?
  if [ $rc -eq 0 ]; then fail "$label" "was allowed as '$out'; expected refusal '$want'"
  elif [ "$out" = "$want" ]; then pass "$label" "refused: $out"
  else fail "$label" "refused, but by '$out', expected '$want'"; fi
}

# --------------------------------------------------------------------------
# AB-B01-3 — the name as a statement, the claim held against it
# --------------------------------------------------------------------------
banner "B-01 — the name as a statement (AB-B01-3, script)"

allowed "B01-3a a work node's name verifies as a statement" n01 ca-eu-c1 work eu-c1 n-01
allowed "B01-3b the control plane's name verifies the same way" n00 ca-eu-c1 control eu-c1 n-00

refused "B01-3c a claim of another cell dies against the name" cell_mismatch \
        n01 ca-eu-c1 work eu-c2 n-01
refused "B01-3d a claim of another role dies against the name" role_mismatch \
        n01 ca-eu-c1 control eu-c1 n-01
refused "B01-3e a claim of another node's id dies against the name" node_mismatch \
        n01 ca-eu-c1 work eu-c1 n-77

banner "B-01 — the name is a statement only under the anchor's signature"

refused "B01-3f a self-signed claim of the cell buys nothing" untrusted \
        imposter ca-eu-c1 work eu-c1 n-66
refused "B01-3g another cell's node fails this cell's anchor" untrusted \
        n02 ca-eu-c1 work eu-c2 n-02
# The control that keeps the anchor honest: the same foreign certificate is lawful where its own
# cell's CA is the anchor — the refusal above was the anchor's, not a refuse-everything verifier's.
allowed "B01-3h the same node verifies under its own cell's anchor" n02 ca-eu-c2 work eu-c2 n-02

refused "B01-3i a certificate without the workpod name has no identity" no_identity \
        nameless ca-eu-c1 work eu-c1 n-03
refused "B01-3j two names are no identity" no_identity \
        twin ca-eu-c1 work eu-c1 n-04
refused "B01-3k a role outside the four is no identity" no_identity \
        oddrole ca-eu-c1 gateway eu-c1 n-05

# --------------------------------------------------------------------------
# The mTLS scaffolding, exercised in both directions (SP-E10-1). openssl s_server stands where the
# control plane will listen, s_client where a node dials; the certificates are the ones above, so
# the handshake and the statement rest on the same material.
# --------------------------------------------------------------------------
banner "B-01/E-10 — mTLS, both sides verify (scaffolding)"

PORT_BASE=$((24400 + RANDOM % 2000))

# s_server relays its stdin into the connection and quits on stdin EOF — under a CI runner stdin
# is at EOF from the start, and -ign_eof does not prevent the quit in OpenSSL 3.0. A fifo opened
# read-write never returns EOF, so it keeps the listener alive without a feeder process.
STDIN_FIFO="$SCRATCH/server-stdin"
mkfifo "$STDIN_FIFO"

start_server() { # port logfile cert-name extra-args...
  local port="$1" log="$2" cert="$3"; shift 3
  timeout 30 openssl s_server -accept "$port" -naccept 1 -tls1_3 \
    -cert "$SCRATCH/$cert.pem" -key "$SCRATCH/$cert.key" "$@" 0<>"$STDIN_FIFO" > "$log" 2>&1 &
  SERVER_PID=$!
}

stop_server() {
  # -naccept 1 lets the server exit by itself after one connection; the kill is for the cases
  # where a refused handshake leaves it waiting, and the loop gives its log time to flush.
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    kill -0 "$SERVER_PID" 2>/dev/null || break
    sleep 0.2
  done
  kill "$SERVER_PID" 2>/dev/null
  wait "$SERVER_PID" 2>/dev/null
  SERVER_PID=""
}

dial() { # port client-args...   sends one line, retries only while the listener is not up yet
  local port="$1"; shift
  local i rc
  for i in $(seq 1 50); do
    printf 'ping\n' | timeout 10 openssl s_client -connect "127.0.0.1:$port" -brief "$@" \
      > "$SCRATCH/client.out" 2> "$SCRATCH/client.err"
    rc=$?
    grep -q 'errno=111\|Connection refused' "$SCRATCH/client.err" || return $rc
    sleep 0.2
  done
  return $rc
}

# Control — the lawful pair: the listener demands a client certificate (-Verify) and the dialer
# verifies the listener (-verify_return_error). Both directions must hold in one handshake: the
# dialer reports the listener verified, and the listener relays the data, which -Verify with
# -verify_return_error only does past a verified client certificate.
PORT=$PORT_BASE
start_server "$PORT" "$SCRATCH/server1.log" n00 -CAfile "$SCRATCH/ca-eu-c1.pem" -Verify 1 -verify_return_error
if dial "$PORT" -cert "$SCRATCH/n01.pem" -key "$SCRATCH/n01.key" -CAfile "$SCRATCH/ca-eu-c1.pem" -verify_return_error \
   && grep -q 'Verification: OK' "$SCRATCH/client.err"; then
  stop_server
  if grep -q 'ping' "$SCRATCH/server1.log"; then
    pass "B01-3l mutual verification carries the connection" "TLS 1.3, both ends verified"
  else
    fail "B01-3l mutual verification carries the connection" "handshake stood, but no data reached the listener"
  fi
else
  stop_server
  fail "B01-3l mutual verification carries the connection" "the lawful handshake did not stand"
fi

# Probe — no anonymous connection on any interface: a dialer without a certificate is refused in
# the TLS layer. The named refusal is the listener's "peer did not return a certificate".
PORT=$((PORT_BASE + 1))
start_server "$PORT" "$SCRATCH/server2.log" n00 -CAfile "$SCRATCH/ca-eu-c1.pem" -Verify 1 -verify_return_error
dial "$PORT" -CAfile "$SCRATCH/ca-eu-c1.pem" -verify_return_error
rc=$?
stop_server
if grep -q 'ping' "$SCRATCH/server2.log"; then
  fail "B01-3m the listener refuses an anonymous dialer" "data crossed without a client certificate"
elif grep -q 'peer did not return a certificate' "$SCRATCH/server2.log"; then
  pass "B01-3m the listener refuses an anonymous dialer" "refused: peer did not return a certificate"
else
  fail "B01-3m the listener refuses an anonymous dialer" "refused, but not for the expected cause (rc=$rc)"
fi

# Probe — a dialer whose certificate the cell CA did not sign is refused the same way, whatever
# its certificate claims. The named refusal is the listener's "certificate verify failed".
PORT=$((PORT_BASE + 2))
start_server "$PORT" "$SCRATCH/server3.log" n00 -CAfile "$SCRATCH/ca-eu-c1.pem" -Verify 1 -verify_return_error
dial "$PORT" -cert "$SCRATCH/imposter.pem" -key "$SCRATCH/imposter.key" -CAfile "$SCRATCH/ca-eu-c1.pem" -verify_return_error
rc=$?
stop_server
if grep -q 'ping' "$SCRATCH/server3.log"; then
  fail "B01-3n the listener refuses an unanchored certificate" "data crossed on an attacker-signed certificate"
elif grep -q 'certificate verify failed' "$SCRATCH/server3.log"; then
  pass "B01-3n the listener refuses an unanchored certificate" "refused: certificate verify failed"
else
  fail "B01-3n the listener refuses an unanchored certificate" "refused, but not for the expected cause (rc=$rc)"
fi

# Probe — the other direction: the dialer refuses a listener the cell CA did not sign, even one
# whose certificate claims the control plane's own name. The named refusal is the dialer's
# "certificate verify failed", made fatal by -verify_return_error before any data flows.
PORT=$((PORT_BASE + 3))
start_server "$PORT" "$SCRATCH/server4.log" rogue -CAfile "$SCRATCH/ca-attacker.pem"
if dial "$PORT" -cert "$SCRATCH/n01.pem" -key "$SCRATCH/n01.key" -CAfile "$SCRATCH/ca-eu-c1.pem" -verify_return_error; then
  stop_server
  fail "B01-3o the dialer refuses an unanchored listener" "the dialer accepted an attacker-signed control plane"
else
  stop_server
  if grep -q 'certificate verify failed' "$SCRATCH/client.err"; then
    pass "B01-3o the dialer refuses an unanchored listener" "refused: certificate verify failed"
  else
    fail "B01-3o the dialer refuses an unanchored listener" "refused, but not for the expected cause"
  fi
fi

# --------------------------------------------------------------------------
banner "Result"
printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
