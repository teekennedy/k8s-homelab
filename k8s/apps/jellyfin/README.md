# Jellyfin

This helm chart contains the jellyfin media server, as well as some supporting services to request, download, and manage the media.

## Manual configuration steps

After deploying the helm chart, you'll need to do some manual steps to setup users and sync API keys:
- Go to prowlarr and add apps for sonarr and radarr. The API keys for sonarr and radarr are in the `api-keys` secret.
- Create your administrator account on jellyfin, then go to https://jellyfin.msng.to/web/#/dashboard/keys and create API keys for jellyseerr, radarr, sonarr, and homepage.
  Create a secret with the value of the homepage API key by copying the key to the clipboard and running `kubectl -n jellyfin create secret generic jellyfin-homepage-api-key --from-literal=api-key="$(pbpaste)"`
  Add the other jellyfin API keys to the corresponding service.
- Qbittorrent will generate a temporary password for the admin user and log it to stdout on startup.
  Login with the temporary username / password and then reset the password. Otherwise it will be reset every time qbittorrent is restarted.
  Add the username and password to the `qbittorrent-credentials` secret.
  Assuming a username of `admin`, you can copy the password to your clipboard and use the following command to setup the secret:
  `kubectl -n jellyfin create secret generic qbittorrent-credentials --from-literal=username="admin" --from-literal=password="$(pbpaste)"`
