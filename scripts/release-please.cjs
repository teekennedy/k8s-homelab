const {createHash} = require('node:crypto');
const {mkdtemp, writeFile, rm} = require('node:fs/promises');
const {tmpdir} = require('node:os');
const Module = require('node:module');
const {resolve} = require('node:path');

// PR #21 fixes release detection after Forgejo deletes a merged PR's branch.
const revision = '61e64a1f49bf1d048a443e1738b54eb34700c85e';
const digest = '408507cccdfc1896b5c807900565b2fae4c059cdc037786fa3c36922a3195472';
const bundleURL = `https://raw.githubusercontent.com/joaquinjsb/gitea-release-please-action/${revision}/dist/index.js`;

function filterDevenvCommits(commitsPerPath, repositoryConfig) {
  for (const [path, config] of Object.entries(repositoryConfig)) {
    if (path !== '.' || config.component !== 'devenv') continue;
    commitsPerPath[path] = commitsPerPath[path].filter(commit => {
      if (!Array.isArray(commit.files)) {
        throw new Error(`Missing changed files for commit ${commit.sha}`);
      }
      return commit.files.some(file =>
        ['devenv.nix', 'devenv.yaml', 'devenv.lock'].includes(file) ||
        file.startsWith('cmd/lab/'));
    });
  }
  return commitsPerPath;
}

function patchBundle(source) {
  if (createHash('sha256').update(source).digest('hex') !== digest) {
    throw new Error('Unexpected Release Please bundle checksum');
  }
  // Filter only version candidates, after release boundaries have been found.
  // Filtering the history iterator would also hide merged release PRs.
  const anchor = 'commitsPerPath = commitExclude.excludeCommits(commitsPerPath);';
  if (source.split(anchor).length !== 2) {
    throw new Error('Release Please commit-filter hook changed');
  }
  // The fork's commits?sha endpoint paginates ancestor commits, not files.
  const start = source.indexOf('this.getCommitFiles = wrapAsync(async (sha) => {');
  const end = source.indexOf('this.graphqlRequest =', start);
  source = source.slice(0, start) + `this.getCommitFiles = wrapAsync(async (sha) => {
            const response = await this.octokit.request('GET /repos/{owner}/{repo}/git/commits/{sha}', {
                owner: this.repository.owner, repo: this.repository.repo, sha
            });
            if (!Array.isArray(response.data.files)) throw new Error('Missing commit files: ' + sha);
            return response.data.files.flatMap(file => [file.filename, file.previous_filename].filter(Boolean));
        });
        ` + source.slice(end);
  source = source.replace('headRefName: pr.head.ref,', 'headRefName: resolveHeadBranchName(pr.head),');
  // The fork leaves merged-PR file lists empty; hydrate them before splitting.
  source = source.replace('commitData.push(commit);', `if (options.backfillFiles && !commit.files?.length) {
                commit.files = await this.getCommitFiles(commit.sha);
            }
            commitData.push(commit);`);
  return source.replace(anchor, `${anchor}\n        commitsPerPath = (${filterDevenvCommits.toString()})(commitsPerPath, this.repositoryConfig);`);
}

function loadBundle(source, filename) {
  const action = new Module(filename, module);
  action.filename = filename;
  action.paths = module.paths;
  source = source.replace("if (require.main === require.cache[eval('__filename')]) {", 'if (false) {');
  action._compile(source, filename);
  return action.exports;
}

const assets = {
  "commit.hbs": "3b50b5b04d5067c0b00b768623c133ccf079c5b742d5ccd466ead967103d68d3",
  "commit1.hbs": "87b17ec541bcadb6771923f323c63b87ea436106578f37565b73e6afad904021",
  "commit2.hbs": "87b17ec541bcadb6771923f323c63b87ea436106578f37565b73e6afad904021",
  "footer.hbs": "de814f47109202cb0f320a6474fb54a5ac6707454a894a205c544d5f1df607c0",
  "footer1.hbs": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "header.hbs": "48e811c21c68c9ffe09c358c01535f96bd1ab4113563295dabe11845e41551d2",
  "header1.hbs": "2437dc15d41a68ed115b66e21a65ae3c6397f764ded3afd2dc28f6a8c19b8209",
  "header2.hbs": "aaa6e2829c1c9cf705b0aca1e00f32b6cbe1d96d0cca3aaea60d283a7f900e5b",
  "template.hbs": "e00ca3ed9c69a4c18802fd54acbe5898191d28495287a285b38c343747528d42",
  "template1.hbs": "6e2dba431e8f781dc9b3d21512f8f31a5c2a0c97244e350bcd9c9aeb977a57dc",
  "template2.hbs": "6e2dba431e8f781dc9b3d21512f8f31a5c2a0c97244e350bcd9c9aeb977a57dc"
};

async function prepareAssets() {
  const directory = await mkdtemp(resolve(tmpdir(), 'release-please-'));
  try {
    await Promise.all(Object.entries(assets).map(async ([name, expected]) => {
      const response = await fetch(new URL(name, bundleURL));
      if (!response.ok) throw new Error(`Downloading ${name}: HTTP ${response.status}`);
      const content = Buffer.from(await response.arrayBuffer());
      if (createHash('sha256').update(content).digest('hex') !== expected) {
        throw new Error(`Unexpected checksum for ${name}`);
      }
      await writeFile(resolve(directory, name), content);
    }));
    return directory;
  } catch (error) {
    await rm(directory, {recursive: true, force: true});
    throw error;
  }
}

async function main() {
  const response = await fetch(bundleURL);
  if (!response.ok) throw new Error(`Downloading Release Please: HTTP ${response.status}`);
  const source = patchBundle(await response.text());
  const inputs = {
    token: process.env.RELEASE_PLEASE_TOKEN,
    'git-username': 'release-please',
    'git-password': process.env.RELEASE_PLEASE_TOKEN,
    'repo-url': 'ops/k8s-homelab',
    'github-api-url': 'https://git.msng.to/api/v1',
    'changelog-host': 'https://git.msng.to',
    'target-branch': 'main',
    'config-file': 'release-please-config.json',
    'manifest-file': '.release-please-manifest.json',
  };
  if (!inputs.token) throw new Error('RELEASE_PLEASE_TOKEN is required');
  for (const [name, value] of Object.entries(inputs)) {
    process.env[`INPUT_${name.toUpperCase()}`] = value;
  }
  const directory = await prepareAssets();
  try {
    await loadBundle(source, resolve(directory, 'index.cjs')).main();
  } finally {
    await rm(directory, {recursive: true, force: true});
  }
}

module.exports = {bundleURL, filterDevenvCommits, patchBundle, loadBundle, prepareAssets};
if (require.main === module) {
  main().catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
