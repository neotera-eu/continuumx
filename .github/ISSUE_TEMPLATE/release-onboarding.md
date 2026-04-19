---
name: Release onboarding
about: Create a checklist of tasks for new release to complete the onboarding process
title: "[RELEASE ONBOARDING] release"
labels: sig/release

---

# New Release Onboarding

This is an issue template aims to provide a comprehensive checklist template for releases onboarding. It includes a detailed list of preparation tasks to complete before the release and additional checkpoints to ensure everything runs smoothly after the release.

## Requirements for release onboarding

- [ ] All CI tests pass.
- [ ] Update ContinuumX version in [manifests](https://github.com/continuumx/continuumx/tree/master/manifests)

## After release onboarding

- [ ] Draft staging repository release.
- [ ] Update [Kubernetes compatibility](https://github.com/continuumx/continuumx?tab=readme-ov-file#kubernetes-compatibility).
- [ ] Release blog.
- [ ] Update ContinuumX [latest version](https://github.com/continuumx/website/blob/master/functions/latestversion.js).
- [ ] Update ContinuumX documentation version.
- [ ] Update Kubernetes version in github action CI. 