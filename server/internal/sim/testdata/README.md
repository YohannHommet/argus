# internal/sim/testdata

## golden/

The SPEC §7.2 / P2-12 determinism AC's committed fixture: "`argusd sim
--out=/tmp/f --seed=7 --sessions=3` twice produces byte-identical files
… asserted by a golden test over `--out`". The committed fixture uses
`--seed=193 --sessions=1 --flush-immediately` instead of the manual
verification command's `--seed=7 --sessions=3`: seed 193 was chosen (by a
brute-force search over seeds 1-500, see below) because it happens to draw
a very small session (1 turn, 0 tool calls) — the AC only requires *some*
1-session golden, and a small one keeps the committed fixture small per
this ticket's instruction ("Keep golden files small"), while still
exercising exactly the same generator → encode → FileTransport path any
other seed would.

Regenerate with:

```sh
cd server
go build -o /tmp/argusd-bin ./cmd/argusd
rm -rf internal/sim/testdata/golden
/tmp/argusd-bin sim --out=internal/sim/testdata/golden --seed=193 --sessions=1 --flush-immediately
```

Then verify byte-identity against a second run before committing:

```sh
/tmp/argusd-bin sim --out=/tmp/golden-check --seed=193 --sessions=1 --flush-immediately
diff -r internal/sim/testdata/golden /tmp/golden-check && echo OK
```

No real identity values are committed here: every `session_id`/`user.*`/
`organization.id` in these fixtures is a synthetic UUID minted from the
seeded PCG stream (attrs.go's `sessionIdentity`), never a real account.

The seed search used to find 193:

```go
for seed := uint64(1); seed < 500; seed++ {
    cfg := DefaultConfig()
    cfg.Seed = seed
    r := generateSession(cfg, NewClock(FixedEpoch), 0, 0, "argus")
    // track the seed with the smallest len(r.Logs)+len(r.Hooks)+len(r.Metrics)
}
```
