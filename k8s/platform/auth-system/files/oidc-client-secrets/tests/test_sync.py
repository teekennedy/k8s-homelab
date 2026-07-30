"""
Unit tests for the Authelia OIDC client secret sync.

These cover the pure planning/parsing logic and the bcrypt format contract; no
Kubernetes cluster is required.
"""

import base64

import pytest

import sync

# A real hash produced by Authelia itself, for a throwaway plaintext:
#   authelia crypto hash generate bcrypt --password 'oidc-sync-test-fixture'
# This pins the cross-implementation contract: if Python's bcrypt ever stopped
# verifying Authelia's output, every sync would silently re-hash and restart
# Authelia forever, and this test is what catches it.
AUTHELIA_PLAINTEXT = "oidc-sync-test-fixture"
AUTHELIA_HASH = "$2b$12$bwb9oTRzvKw4Y1WoIeFZTOfIKlQDqEt6jWD8QEOVZwoU.rzstgRmC"


class TestParseClientSources:
    def test_parses_newline_separated_entries(self):
        raw = """
        oauth2-proxy=oauth2-proxy/oauth2-proxy-secrets
        copyparty=copyparty/copyparty-oauth2-proxy-secrets
        """
        sources = sync.parse_client_sources(raw, "client-secret")
        assert set(sources) == {"oauth2-proxy", "copyparty"}
        assert sources["copyparty"] == sync.SourceRef(
            "copyparty", "copyparty-oauth2-proxy-secrets", "client-secret"
        )

    def test_parses_comma_separated_entries(self):
        sources = sync.parse_client_sources("a=ns1/s1,b=ns2/s2", "client-secret")
        assert set(sources) == {"a", "b"}

    def test_default_key_is_applied(self):
        sources = sync.parse_client_sources("a=ns/s", "client-secret")
        assert sources["a"].key == "client-secret"

    def test_explicit_key_overrides_default(self):
        sources = sync.parse_client_sources("a=ns/s:custom-key", "client-secret")
        assert sources["a"].key == "custom-key"

    def test_reflects_is_namespace_slash_name(self):
        sources = sync.parse_client_sources("a=ns/s", "client-secret")
        assert sources["a"].reflects == "ns/s"

    def test_ignores_blanks_and_comments(self):
        raw = "\n# a comment\n\na=ns/s\n"
        assert set(sync.parse_client_sources(raw, "client-secret")) == {"a"}

    def test_empty_input_yields_nothing(self):
        assert sync.parse_client_sources("", "client-secret") == {}

    @pytest.mark.parametrize(
        "raw",
        [
            "no-equals-sign",
            "a=missing-namespace",
            "a=/no-namespace",
            "a=ns/",
            "=ns/s",
        ],
    )
    def test_rejects_malformed_entries(self, raw):
        with pytest.raises(ValueError):
            sync.parse_client_sources(raw, "client-secret")

    def test_rejects_duplicate_client_ids(self):
        with pytest.raises(ValueError, match="duplicate"):
            sync.parse_client_sources("a=ns/s1\na=ns/s2", "client-secret")


class TestNeedsRehash:
    def test_verifies_hash_generated_by_authelia(self):
        assert sync.needs_rehash(AUTHELIA_PLAINTEXT, AUTHELIA_HASH) is False

    def test_missing_hash_needs_rehash(self):
        assert sync.needs_rehash("secret", None) is True

    def test_empty_hash_needs_rehash(self):
        assert sync.needs_rehash("secret", "") is True

    def test_wrong_plaintext_needs_rehash(self):
        assert sync.needs_rehash("rotated", AUTHELIA_HASH) is True

    def test_non_bcrypt_placeholder_needs_rehash(self):
        assert sync.needs_rehash("secret", "REPLACE_ME") is True

    def test_own_hash_round_trips(self):
        assert sync.needs_rehash("secret", sync.hash_secret("secret", 4)) is False


class TestHashSecret:
    def test_uses_2b_prefix_and_requested_cost(self):
        assert sync.hash_secret("secret", 4).startswith("$2b$04$")

    def test_hashes_are_salted_so_never_identical(self):
        assert sync.hash_secret("secret", 4) != sync.hash_secret("secret", 4)


class TestPlanTargetChanges:
    def test_unchanged_secret_is_a_noop(self):
        existing = {"a": sync.hash_secret("plain-a", 4)}
        patch, restart = sync.plan_target_changes(existing, {"a": "plain-a"}, 4)
        assert patch == {}
        assert restart is False

    def test_new_client_is_added_without_restart(self):
        # Adding a client also changes Authelia's ConfigMap, and the chart's
        # checksum annotation rolls the Deployment for that on its own.
        patch, restart = sync.plan_target_changes({}, {"a": "plain-a"}, 4)
        assert set(patch) == {"a"}
        assert sync.needs_rehash("plain-a", patch["a"]) is False
        assert restart is False

    def test_rotated_secret_is_updated_and_forces_restart(self):
        existing = {"a": sync.hash_secret("old", 4)}
        patch, restart = sync.plan_target_changes(existing, {"a": "new"}, 4)
        assert sync.needs_rehash("new", patch["a"]) is False
        assert restart is True

    def test_unconfigured_client_is_removed(self):
        existing = {"gone": sync.hash_secret("x", 4)}
        patch, restart = sync.plan_target_changes(existing, {}, 4)
        assert patch == {"gone": None}
        assert restart is False

    def test_removal_does_not_force_restart(self):
        existing = {
            "keep": sync.hash_secret("k", 4),
            "gone": sync.hash_secret("g", 4),
        }
        patch, restart = sync.plan_target_changes(existing, {"keep": "k"}, 4)
        assert patch == {"gone": None}
        assert restart is False

    def test_invalid_existing_hash_is_replaced_and_restarts(self):
        # The pre-automation hand-written placeholders look like this.
        patch, restart = sync.plan_target_changes(
            {"a": "not-a-hash"}, {"a": "plain-a"}, 4
        )
        assert sync.needs_rehash("plain-a", patch["a"]) is False
        assert restart is True

    def test_mixed_add_update_and_remove(self):
        existing = {
            "stable": sync.hash_secret("s", 4),
            "rotated": sync.hash_secret("old", 4),
            "gone": "x",
        }
        plaintexts = {"stable": "s", "rotated": "new", "fresh": "f"}
        patch, restart = sync.plan_target_changes(existing, plaintexts, 4)
        assert set(patch) == {"rotated", "fresh", "gone"}
        assert patch["gone"] is None
        assert restart is True


class TestEncoding:
    def test_decode_data_decodes_base64(self):
        class FakeSecret:
            data = {"k": base64.b64encode(b"value").decode()}

        assert sync.decode_data(FakeSecret()) == {"k": "value"}

    def test_decode_data_handles_empty_secret(self):
        class FakeSecret:
            data = None

        assert sync.decode_data(FakeSecret()) == {}

    def test_encode_patch_preserves_none_removals(self):
        assert sync.encode_patch({"a": "value", "b": None}) == {
            "a": base64.b64encode(b"value").decode(),
            "b": None,
        }


class TestMirrorName:
    def test_prefixes_client_id(self):
        assert (
            sync.mirror_name("oidc-client-mirror-", "copyparty")
            == "oidc-client-mirror-copyparty"
        )
