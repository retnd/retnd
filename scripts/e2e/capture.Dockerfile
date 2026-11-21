# The capture rig's image (#998, #1543): the pinned Playwright image plus
# ffmpeg, which docs/site/tools/harness.mjs needs for the GIFs.
#
# Until #1543 this was `retnd-capture:1.62.1`, built by hand on the
# maintainer's Mac with no Dockerfile anywhere, so the capture controls of the
# agents and encryption self-tests could not run on any other machine. Now
# scripts/e2e/ui-capture.sh builds it from this file, tags it with the file's
# own hash (so an edit here is a new image, never a stale one under an old
# tag), and hands it to the Chromium lane (scripts/e2e/ui-browser.sh) as
# RETND_UI_BROWSER_IMAGE. Nothing else about the lane changes.
#
# Pinned twice, so two builds of this file are the same image:
#   * the base by digest, the same OCI index digest scripts/e2e/client-machine.Dockerfile,
#     scripts/contrast/run-browser-check.sh and the lane pin (it resolves on
#     amd64 and arm64 alike);
#   * the packages by an Ubuntu archive snapshot, so ffmpeg and everything it
#     pulls in are the versions the archive had at that instant, not whatever
#     it has the day it is built. The sources are replaced outright with the
#     snapshot's URL: the base image points apt at a cloud mirror
#     (azure.archive.ubuntu.com), and apt's own APT::Snapshot switch leaves a
#     mirror alone, so it pinned nothing (#1543's first build fetched from the
#     live mirror). The nodesource list goes too; node is already in the base.
#
# The tag has to keep `:vX.Y.Z-` in it: the lane reads the Playwright version
# out of the image name to install the @playwright/test that matches the
# browsers in /ms-playwright. ui-capture.sh reads it from the FROM line below,
# so moving the base is this file only.
#
# Moving the snapshot is this file only too: pick a later instant from
# https://snapshot.ubuntu.com and change APT_SNAPSHOT.
FROM mcr.microsoft.com/playwright:v1.62.1-noble@sha256:dcc5531e97840b9b5e794f2814476b21571c5124a3fca2267d73041f56e7580e

ARG APT_SNAPSHOT=20261115T000000Z

RUN rm -f /etc/apt/sources.list /etc/apt/sources.list.d/* \
 && printf 'Types: deb\nURIs: https://snapshot.ubuntu.com/ubuntu/%s\nSuites: noble noble-updates noble-security\nComponents: main universe\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\n' \
      "${APT_SNAPSHOT}" >/etc/apt/sources.list.d/snapshot.sources \
 && apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg \
 && rm -rf /var/lib/apt/lists/* \
 && ffmpeg -hide_banner -version | head -n 1
