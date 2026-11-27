# The simulated VPS being backed up, and the one definition of it (#451).
#
# Both things that stand a source machine up read this file: the Go machine
# tier through core/tests/machines, and scripts/e2e/two-machine-backup.sh
# through `docker build -f`. Before this file existed each had its own copy
# of the same two lines, which is two definitions of "the simulated VPS"
# waiting to disagree about what the simulated VPS is.
#
# atmoz/sftp is OpenSSH's sshd, chrooted and forced into internal-sftp: a
# genuine SSH endpoint with real host-key verification and real chroot and
# permission semantics, which is the posture docs/ssh-setup.md recommends.
#
# iptables is what LimitConnections needs to impose #264's connection cap
# from inside the container, with --cap-add NET_ADMIN rather than
# --privileged. netcat is what lets that cap be probed from a second machine
# on the same network, which is how the harness proves the rule bites before
# a test is allowed to trust it.
#
# The keepalives are #455. A host process reaches this machine through a
# published port, and Docker Desktop's forwarder now and then drops the
# client's close, so sshd kept an ESTABLISHED row for a client that was long
# gone: a leak in the connection-count tests and a used slot under the
# connlimit cap. With a keepalive every second sshd writes to an idle peer,
# a forwarder that lost the close fails that write and takes the leg down,
# and a peer that answers nothing at all is dropped after three misses. A
# live client answers each one, so a real leak still holds its row.
FROM atmoz/sftp:alpine
RUN apk add --no-cache iptables netcat-openbsd \
 && printf '\nClientAliveInterval 1\nClientAliveCountMax 3\n' >> /etc/ssh/sshd_config
