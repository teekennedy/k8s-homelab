import server


def _conversation(status, model, cost, prompt=0, completion=0, cache_read=0):
    return {
        "execution_status": status,
        "stats": {
            "usage_to_metrics": {
                "default": {
                    "model_name": model,
                    "accumulated_cost": cost,
                    "accumulated_token_usage": {
                        "prompt_tokens": prompt,
                        "completion_tokens": completion,
                        "cache_read_tokens": cache_read,
                    },
                }
            }
        },
    }


def _pages(monkeypatch, pages):
    """Serve `pages` in order from the paginated search endpoint."""
    calls = []

    def fake_get(path, params, api_key):
        calls.append((path, dict(params)))
        return pages[len(calls) - 1]

    monkeypatch.setattr(server, "_get", fake_get)
    return calls


class TestCollect:
    def test_it_folds_usage_across_conversations(self, monkeypatch):
        _pages(
            monkeypatch,
            [
                {
                    "items": [
                        _conversation("finished", "opus", 0.25, 10, 100, 1000),
                        _conversation("finished", "opus", 0.75, 5, 50, 500),
                        _conversation("running", "sonnet", 0.10, 1, 2, 3),
                    ],
                    "next_page_id": None,
                }
            ],
        )

        snapshot = server.collect(None)

        assert snapshot["total"] == 3
        assert snapshot["by_status"] == {"finished": 2, "running": 1}
        assert snapshot["cost"]["opus"] == 1.0
        assert snapshot["cost"]["sonnet"] == 0.10
        assert snapshot["tokens"]["opus\x00completion_tokens"] == 150
        assert snapshot["tokens"]["opus\x00cache_read_tokens"] == 1500

    def test_it_follows_every_page(self, monkeypatch):
        calls = _pages(
            monkeypatch,
            [
                {
                    "items": [_conversation("finished", "opus", 1.0)],
                    "next_page_id": "p2",
                },
                {
                    "items": [_conversation("finished", "opus", 2.0)],
                    "next_page_id": None,
                },
            ],
        )

        snapshot = server.collect(None)

        assert snapshot["total"] == 2
        assert snapshot["cost"]["opus"] == 3.0
        assert calls[1][1]["page_id"] == "p2"

    def test_a_conversation_with_no_usage_still_counts(self, monkeypatch):
        _pages(monkeypatch, [{"items": [{"execution_status": "running"}]}])

        snapshot = server.collect(None)

        assert snapshot["total"] == 1
        assert snapshot["by_status"] == {"running": 1}
        assert snapshot["cost"] == {}

    def test_a_missing_status_is_labelled_unknown(self, monkeypatch):
        _pages(monkeypatch, [{"items": [{"stats": {}}]}])

        assert server.collect(None)["by_status"] == {"unknown": 1}


class TestRender:
    def test_it_emits_one_series_per_model_and_kind(self):
        snapshot = {
            "total": 1,
            "by_status": {"finished": 1},
            "cost": {"opus": 1.5},
            "tokens": {"opus\x00completion_tokens": 42},
        }

        out = server.render(snapshot, scrape_errors=0, scraped_at=1.0)

        assert 'openhands_conversations{status="finished"} 1' in out
        assert "openhands_conversations_total 1" in out
        assert 'openhands_usage_cost_usd{model="opus"} 1.500000' in out
        # The _tokens suffix is dropped: it is already in the metric name.
        assert 'openhands_usage_tokens{model="opus",kind="completion"} 42' in out

    def test_every_help_line_has_a_matching_type_line(self):
        out = server.render(
            {"total": 0, "by_status": {}, "cost": {}, "tokens": {}}, 0, 0.0
        )

        helps = [ln.split()[2] for ln in out.splitlines() if ln.startswith("# HELP ")]
        types = [ln.split()[2] for ln in out.splitlines() if ln.startswith("# TYPE ")]
        assert helps == types

    def test_a_label_value_cannot_break_out_of_its_quotes(self):
        out = server.render(
            {"total": 0, "by_status": {}, "cost": {'ev"il': 1.0}, "tokens": {}}, 0, 0.0
        )

        assert 'model="ev\\"il"' in out


class TestCollector:
    def test_a_failed_scrape_keeps_the_previous_snapshot(self, monkeypatch):
        collector = server.Collector()
        monkeypatch.setattr(server, "read_api_key", lambda: None)

        monkeypatch.setattr(
            server,
            "collect",
            lambda _key: {
                "total": 7,
                "by_status": {"finished": 7},
                "cost": {},
                "tokens": {},
            },
        )
        assert "openhands_conversations_total 7" in collector.render()

        def boom(_key):
            raise server.ScrapeError("agent server is down")

        monkeypatch.setattr(server, "collect", boom)
        monkeypatch.setattr(server, "MIN_SCRAPE_INTERVAL", 0.0)

        out = collector.render()
        assert "openhands_conversations_total 7" in out
        assert "openhands_metrics_scrape_errors_total 1" in out

    def test_it_serves_the_cache_inside_the_minimum_interval(self, monkeypatch):
        collector = server.Collector()
        monkeypatch.setattr(server, "read_api_key", lambda: None)
        calls = []

        def counting(_key):
            calls.append(1)
            return {"total": len(calls), "by_status": {}, "cost": {}, "tokens": {}}

        monkeypatch.setattr(server, "collect", counting)
        monkeypatch.setattr(server, "MIN_SCRAPE_INTERVAL", 3600.0)

        collector.render()
        collector.render()

        assert len(calls) == 1
