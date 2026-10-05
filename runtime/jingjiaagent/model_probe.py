"""Minimal live-model check. Reads credentials from an ignored local file."""
import argparse
import json
import urllib.error
import urllib.request


def probe(config):
    payload = {"model": config["model"], "messages": [{"role": "user", "content": "Reply exactly JINGJIAAGENT_RUNTIME_MODEL_OK"}],
               "max_tokens": 64, "stream": False, "thinking": {"type": "disabled"}}
    req = urllib.request.Request(config["base_url"].rstrip("/") + "/chat/completions",
                                 data=json.dumps(payload).encode(), method="POST",
                                 headers={"Authorization": "Bearer " + config["api_key"], "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=90) as res:
            data = json.load(res)
            print(json.dumps({"ok": True, "requested_model": config["model"], "served_model": data.get("model"),
                              "reply": data.get("choices", [{}])[0].get("message", {}).get("content")}, ensure_ascii=False))
    except urllib.error.HTTPError as e:
        data = json.load(e)
        error = data.get("error", {})
        print(json.dumps({"ok": False, "status": e.code, "error_type": error.get("type"),
                          "error_code": error.get("code"), "message": error.get("message")}, ensure_ascii=False))
        raise SystemExit(1)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", default=".state/model.json")
    args = parser.parse_args()
    with open(args.config, encoding="utf-8") as f:
        probe(json.load(f))
