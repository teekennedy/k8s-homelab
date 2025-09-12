# # Lan module
# 
# This module manages devices, networks, and firewall rules for my lan environment.
#
# ## Setup
# 
# ### Authentication
#
# - Login to the unifi console at https://unifi.ui.com and select your network. and click on the _Settings_ gear icon near the bottom of the left sidepanel.
# - On the left, click on _Control Plane_.
# - Click on the _Integrations_ tab.
# - Enter a name and expiration for the new API key and click _Create API Key_.
# - Copy _tfvars.sops.example.yaml_ to _tfvars.sops.yaml_.
# - Set `unifi_api_key` in _tfvars.sops.yaml_ to your newly generated API key.
# - Encrypt the file with `sops encrypt --in-place tfvars.sops.yaml`.
