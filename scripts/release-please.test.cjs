const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {rm} = require('node:fs/promises');
const {resolve} = require('node:path');
const {test} = require('node:test');
const {bundleURL, filterDevenvCommits, patchBundle, loadBundle, prepareAssets} = require('./release-please.cjs');

const config = {'.': {component: 'devenv'}, other: {component: 'other'}};
const commit = (...files) => ({sha: 'abc', message: 'fix: tools', files});

test('exact input paths, mixed commits, empty commits, and future packages', () => {
  const included = ['devenv.nix', 'devenv.yaml', 'devenv.lock', 'cmd/lab/main.go', 'cmd/lab/nested/file'];
  const excluded = ['README.md', 'devenv-version.txt', 'devenv-CHANGELOG.md', '.release-please-manifest.json', 'devenv.local.nix', 'nested/devenv.nix', 'cmd/laboratory/main.go', '.dagger/main.go', 'future/new/file'];
  const wanted = included.map(file => commit(file));
  wanted.push(commit('README.md', 'cmd/lab/main.go'));
  const other = [commit('other/file')];
  const result = filterDevenvCommits({'.': [...wanted, ...excluded.map(file => commit(file)), commit()], other}, config);
  assert.deepEqual(result['.'], wanted);
  assert.equal(result.other, other);
  assert.throws(() => filterDevenvCommits({'.': [{sha: 'missing'}]}, config), /Missing changed files/);
});

test('pinned bundle, real conventional versioning, tags, and deleted branch fix', async t => {
  const directory = await prepareAssets();
  t.after(() => rm(directory, {recursive: true, force: true}));
  const response = process.env.RELEASE_PLEASE_BUNDLE ? null : await fetch(bundleURL);
  if (response) assert.equal(response.ok, true);
  const original = response ? await response.text() : readFileSync(process.env.RELEASE_PLEASE_BUNDLE, 'utf8');
  assert.throws(() => patchBundle(original + ' '), /checksum/);
  // Expose the bundled library in memory; no HTTP client or strategy is mocked.
  const exposed = patchBundle(original)
    .replace('exports.main = void 0;', 'exports.main = void 0; exports.library = __nccwpck_require__(75833); exports.internals = __nccwpck_require__;')
    .replace('exports.GitHub = GitHub;', 'exports.GitHub = GitHub; exports.resolveHeadBranchName = resolveHeadBranchName;');
  const {library, internals} = loadBundle(exposed, resolve(directory, 'index.cjs'));
  const githubID = [...original.slice(0, original.indexOf('class GitHub {')).matchAll(/\/\*\*\*\/ (\d+):/g)].at(-1)[1];
  const {resolveHeadBranchName} = internals(githubID);
  assert.equal(resolveHeadBranchName({ref: 'refs/pull/10/head', label: 'release-please--branches--main--components--devenv'}), 'release-please--branches--main--components--devenv');
  assert.equal(resolveHeadBranchName({ref: 'branch', label: 'owner:branch'}), 'branch');
  assert.equal(resolveHeadBranchName({ref: 'branch'}), 'branch');
  assert.equal(typeof library.Manifest, 'function');
  if (process.env.RELEASE_PLEASE_LIVE_TEST === '1') {
    const live = await library.GitHub.create({owner: 'ops', repo: 'k8s-homelab', defaultBranch: 'main', apiUrl: 'https://git.msng.to/api/v1', token: process.env.FORGEJO_TOKEN});
    const sha = '48bf5011fe368b527d5713360809ab0475cddff1';
    assert.deepEqual(await live.getCommitFiles(sha), ['renovate.json']);
    const page = await live.mergeCommitsGraphQL(sha, undefined, {maxResults: 1, backfillFiles: true});
    assert.deepEqual(page.data[0].files, ['renovate.json']);
    assert.equal(filterDevenvCommits({'.': page.data}, config)['.'].length, 0);
  }
  const {Version} = internals(25112);
  const {DefaultVersioningStrategy} = internals(77033);
  const {parseConventionalCommits} = internals(50726);
  const {TagName} = internals(91203);
  for (const [message, expected] of [
    ['fix: tools', '1.2.4'],
    ['feat: tools', '1.3.0'],
    ['feat: tools\n\nBREAKING CHANGE: remove old CLI', '2.0.0'],
  ]) {
    const commits = parseConventionalCommits([{...commit('devenv.nix'), message}]);
    const version = new DefaultVersioningStrategy().bump(Version.parse('1.2.3'), commits);
    assert.equal(version.toString(), expected);
    assert.equal(new TagName(version, 'devenv', '-', false).toString(), `devenv-${expected}`);
  }
  const {Simple} = internals(13671);
  const github = await library.GitHub.create({owner: 'ops', repo: 'k8s-homelab', defaultBranch: 'main'});
  const strategy = new Simple({github, targetBranch: 'main', path: '.', component: 'devenv', packageName: 'devenv', includeVInTag: false, tagSeparator: '-'});
  const unrelated = filterDevenvCommits({'.': [commit('README.md')]}, config)['.'];
  assert.equal(await strategy.buildReleasePullRequest(parseConventionalCommits(unrelated)), undefined);
  const latest = {tag: new TagName(Version.parse('1.2.3'), 'devenv', '-', false), sha: 'previous', notes: ''};
  const candidate = await strategy.buildReleasePullRequest(parseConventionalCommits([commit('devenv.nix')]), latest);
  assert.equal(candidate.version.toString(), '1.2.4');
  assert.equal(candidate.updates.length, 2);
});
