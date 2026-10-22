# Live round trip

`proxy_live.sh` runs the remote agtermctl proxy end to end against a real host and the running agterm. It is not part of `make check`.

## Needs

- `homelab` reachable over ssh without a prompt (`AGR_LIVE_HOST=<host>` for another), with tmux and python3
- the Mac's agterm running, `agtermctl` and python3 on PATH, run from a shell in agterm
- `bin/agr` built (`make build`)
- a running `agr daemon`, started by hand (a launchd `KeepAlive` job would respawn it mid-run)

## Run

```sh
tests/live/proxy_live.sh
```

It stops the running daemon, starts `bin/agr daemon`, runs `bin/agr install homelab`, runs the checks with throwaway rows named `agrlive-*`, then restores everything. Exit 0 = all passed, 1 = a check failed (named on stderr), 2 = `homelab or agterm unreachable` (nothing was changed).

The `session new` check races the attach; if it fails on the retry message, run again.

The restore trap has its own test, which needs neither host nor agterm:

```sh
tests/live/restore_test.sh
```

## Restore by hand

A SIGKILL skips the trap. Then, with `agr` being the one you normally use:

```sh
pkill -f 'bin/agr daemon'      # the test daemon, if still running
agr install homelab            # puts the previous remote script back
agr daemon &                   # or however you normally start it
agr kill homelab $(agr ls homelab | awk '/^agrlive-/ {print $1}')
```

Close leftover `agrlive-*` rows in agterm, and `rm -rf ~/.claude/skills/agr` on homelab if you had no `agr` skill there before. `/tmp/agrlive-*` on homelab is scratch.
