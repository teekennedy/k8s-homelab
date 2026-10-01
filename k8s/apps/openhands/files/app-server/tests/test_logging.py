import logging

from app_server.main import RedactSecrets


def test_session_keys_are_redacted_from_logged_urls():
    record = logging.LogRecord(
        "uvicorn.error",
        logging.INFO,
        __file__,
        1,
        '%s - "WebSocket %s" [accepted]',
        ("127.0.0.1:1", "/runtime/sbx-1/sockets/events/c?session_api_key=s3cr3t&x=1"),
        None,
    )
    assert RedactSecrets().filter(record)
    message = record.getMessage()
    assert "s3cr3t" not in message
    assert "session_api_key=<redacted>&x=1" in message


def test_records_without_secrets_are_untouched():
    record = logging.LogRecord("x", logging.INFO, __file__, 1, "a %s", ("b",), None)
    RedactSecrets().filter(record)
    assert (record.msg, record.args) == ("a %s", ("b",))
