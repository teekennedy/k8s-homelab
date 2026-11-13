// Merges this chart's OIDC provider into Seerr's settings.json.
//
// SeerrNG reads OIDC providers only from settings.json and has no API or UI
// for them, and the upstream chart takes no init containers. So this is
// preloaded into the server process (NODE_OPTIONS=--import) and runs before
// Seerr loads its settings. Every other node process in the container (the
// exec probes, the migration script) skips it.
import fs from 'node:fs';
import path from 'node:path';

const SERVER_ENTRY = '/app/dist/index.js';

const apply = () => {
  const settingsPath = path.join(
    process.env.CONFIG_DIRECTORY ?? '/app/config',
    'settings.json'
  );
  const provider = JSON.parse(process.env.SEERR_OIDC_PROVIDER);
  const clientSecret = process.env.SEERR_OIDC_CLIENT_SECRET;
  if (!clientSecret) {
    throw new Error('SEERR_OIDC_CLIENT_SECRET is empty');
  }
  provider.clientSecret = clientSecret;

  // Missing on first start. Seerr fills in every default around what is
  // written here.
  let settings = {};
  try {
    settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }

  settings.main = {
    ...settings.main,
    oidcLogin: true,
    // Seerr builds OIDC redirect URIs from this, and Authelia only accepts the
    // ones registered for the client.
    applicationUrl: process.env.SEERR_APPLICATION_URL,
  };
  settings.oidc = {
    ...settings.oidc,
    providers: [
      ...(settings.oidc?.providers ?? []).filter(
        (existing) => existing.slug !== provider.slug
      ),
      provider,
    ],
  };

  const temporaryPath = `${settingsPath}.oidc-settings.tmp`;
  fs.writeFileSync(temporaryPath, `${JSON.stringify(settings, null, 2)}\n`, {
    mode: 0o600,
  });
  fs.renameSync(temporaryPath, settingsPath);
  console.log(`[oidc-settings] applied provider "${provider.slug}"`);
};

if (process.argv[1] && path.resolve(process.argv[1]) === SERVER_ENTRY) {
  apply();
}
