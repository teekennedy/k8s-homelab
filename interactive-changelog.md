# Interactive Changelog

Keeping track of the things I setup or change manually (outside of IaC) so I know what extra steps are involved if I ever need to recreate the cluster from scratch.

Yes, in an ideal world, this list would be empty, but not everything is worth taking the time to automate or declaratively configure.

## Jellyfin downloader

- 2025-04-07: Throttled network to 10 KiB/s between 12:59 and 7:01pm every day to save energy during peak hours.

## Renovate

- 2025-03-29: Created a classic Personal Access Token in GitHub with the `repo:public_repo` scope and added it as a new secret in the global-secrets namespace called `github.renovate`.
  Updated the renovate-secret external secret definition to include this value.
