import copy
import sys

import pytest
import routing


def test_allow_ru_removes_owned_refusal_and_preserves_other_restrictions(monkeypatch, tmp_path):
    template = {"routing": {"rules": [
        {"ruleTag": "3xui-stack-ru-refuse", "domain": ["geosite:tld-ru"], "outboundTag": "blocked"},
        {"ip": ["geoip:private"], "outboundTag": "blocked"},
        {"protocol": ["bittorrent"], "outboundTag": "blocked"},
    ]}}
    expected = copy.deepcopy(template["routing"]["rules"][1:])
    class Panel:
        def login(self):
            return self
    saved = []
    monkeypatch.setattr(routing, "Panel", lambda env: Panel())
    monkeypatch.setattr(routing, "env_file", lambda: {"RU_POLICY": "allow"})
    monkeypatch.setattr(routing, "get_template", lambda p: template)
    monkeypatch.setattr(routing, "save_template", lambda p, value: saved.append(copy.deepcopy(value)) or {"success": True})
    monkeypatch.setattr(routing, "validate", lambda *a: pytest.fail("Allow mode must not require RU geodata"))
    monkeypatch.setattr(routing, "xray_healthy", lambda *a: True)
    monkeypatch.setattr(routing.time, "sleep", lambda *a: None)
    monkeypatch.setattr(routing, "ROOT", str(tmp_path))
    monkeypatch.setattr(sys, "argv", ["routing.py", "--apply"])
    routing.main()
    assert saved[0]["routing"]["rules"] == expected


def test_allow_ru_profile_keeps_private_bypass_without_ru_categories():
    profile = routing.happ_profile({"RU_POLICY": "allow", "BASE_DOMAIN": "example.com"})
    assert profile["DirectSites"] == ["domain:example.com"]
    assert profile["DirectIp"] == ["geoip:private"]
    assert routing.refuse_ru({}) is True
    with pytest.raises(ValueError):
        routing.refuse_ru({"RU_POLICY": "typo"})
