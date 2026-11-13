// Multiplies SeerrNG's per-minute API rate limits by SEERR_RATE_LIMIT_SCALE.
//
// SeerrNG hardcodes them, and some are too low for ordinary browsing. Every
// title card fetches /api/v1/movie/<id> or /api/v1/tv/<id>, and those share a
// budget of 60 a minute per user, so a few pages of search results or Discover
// shelves end in 429s ("Movie details could not be fetched"). Search itself
// allows 30 a minute per user. Limiters with longer windows, which guard
// sign-in, password changes, and uploads, are left alone.
//
// Preloaded with NODE_OPTIONS=--require, so it replaces express-rate-limit in
// the module cache before the server first loads it. Every other node process
// in the container (the exec probes, the migration script) skips it.
'use strict';

const path = require('node:path');
const { createRequire } = require('node:module');

const SERVER_ENTRY = '/app/dist/index.js';
// express-rate-limit's own default window.
const DEFAULT_WINDOW_MS = 60 * 1000;
const MAX_SCALED_WINDOW_MS = 60 * 1000;

const scaleOptions = (options, scale) => {
  const windowMs = options?.windowMs ?? DEFAULT_WINDOW_MS;
  if (windowMs > MAX_SCALED_WINDOW_MS) {
    return options;
  }
  const scaled = { ...options };
  // `max` is the option's older name. A function limit is left as it is.
  for (const key of ['limit', 'max']) {
    if (typeof scaled[key] === 'number') {
      scaled[key] = Math.ceil(scaled[key] * scale);
    }
  }
  return scaled;
};

const install = (scale) => {
  const serverRequire = createRequire(SERVER_ENTRY);
  const resolved = serverRequire.resolve('express-rate-limit');
  const original = serverRequire(resolved);

  // The module's export is rateLimit itself, with its other exports (and
  // `default`, which compiled TypeScript calls) as properties.
  const rateLimit = function rateLimit(options) {
    return original(scaleOptions(options, scale));
  };
  const { length, name, prototype, ...properties } =
    Object.getOwnPropertyDescriptors(original);
  Object.defineProperties(rateLimit, properties);
  rateLimit.default = rateLimit;
  rateLimit.rateLimit = rateLimit;

  require.cache[resolved].exports = rateLimit;
  console.log(`[rate-limits] per-minute API rate limits scaled by ${scale}`);
};

if (process.argv[1] && path.resolve(process.argv[1]) === SERVER_ENTRY) {
  const scale = Number(process.env.SEERR_RATE_LIMIT_SCALE ?? '1');
  if (!Number.isFinite(scale) || scale <= 0) {
    throw new Error('SEERR_RATE_LIMIT_SCALE must be a positive number');
  }
  if (scale !== 1) {
    install(scale);
  }
}
