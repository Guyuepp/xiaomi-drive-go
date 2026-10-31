"""SMS-code login probe for account.xiaomi.com (service: i.mi.com).

  python3 sms_login.py send <phone>      -> sends ONE SMS, saves state
  python3 sms_login.py verify <code>     -> checks the code, logs in, follows location, tests drive access

Never prints tokens or the phone number. State/result files are mode 600.
"""
import base64
import json
import os
import random
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
STATE = "/tmp/mi_auth/sms_state.json"
RESULT = "/tmp/mi_auth/sms_result.json"
ACCT = "https://account.xiaomi.com"
PUB = ("MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCYEVrK/4Mahiv0pUJgTybx4J9P5dUT/Y0PuwMbk+gMU+jrZnBiXGv6/hCH1avIhoBcE535F8nJQQN3Ua"
       "vZdFkYidsoXuEnat3+eVTp3FslyhRwIBDF09v4vDhRtxFOT+R7uH7h/mzmyA2/+lfIMWGIrffXprYizbV76+YQKhoqFQIDAQAB")
ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*"
IV = b"0102030405060708"


# ---------------- crypto (same as the web client) ----------------
def rsa_pkcs1(data: bytes) -> bytes:
    pem = "-----BEGIN PUBLIC KEY-----\n" + "\n".join(PUB[i:i + 64] for i in range(0, len(PUB), 64)) + "\n-----END PUBLIC KEY-----\n"
    with tempfile.TemporaryDirectory() as d:
        k = os.path.join(d, "k.pem")
        open(k, "w").write(pem)
        p = subprocess.run(["openssl", "pkeyutl", "-encrypt", "-pubin", "-inkey", k, "-pkeyopt", "rsa_padding_mode:pkcs1"],
                           input=data, capture_output=True)
        if p.returncode:
            raise RuntimeError(p.stderr.decode())
        return p.stdout


def aes_cbc(key: bytes, data: bytes) -> bytes:
    p = subprocess.run(["openssl", "enc", "-aes-128-cbc", "-K", key.hex(), "-iv", IV.hex()], input=data, capture_output=True)
    if p.returncode:
        raise RuntimeError(p.stderr.decode())
    return p.stdout


def encrypt_fields(fields: dict):
    key = "".join(random.choice(ALPHABET) for _ in range(16))
    eui = base64.b64encode(rsa_pkcs1(base64.b64encode(key.encode()))).decode() + "." + base64.b64encode(",".join(fields).encode()).decode()
    return eui, {k: base64.b64encode(aes_cbc(key.encode(), v.encode())).decode() for k, v in fields.items()}


# ---------------- http with a tiny cookie jar ----------------
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


op = urllib.request.build_opener(NoRedirect)
jar = {}


def _store(host, headers):
    for c in headers.get_all("Set-Cookie") or []:
        first = c.split(";", 1)[0]
        if "=" not in first:
            continue
        n, v = first.split("=", 1)
        attrs = {a.split("=", 1)[0].strip().lower(): (a.split("=", 1)[1] if "=" in a else "") for a in c.split(";")[1:]}
        dom = attrs.get("domain", host).lstrip(".")
        if v == "" or ("expires" in attrs and "1994" in attrs["expires"]):
            jar.pop((n, dom), None)
        else:
            jar[(n, dom)] = v


def _cookie(host):
    return "; ".join(f"{n}={v}" for (n, d), v in jar.items() if host == d or host.endswith("." + d))


def http(method, url, form=None, headers=None, timeout=30):
    host = urllib.parse.urlparse(url).hostname
    h = {"User-Agent": UA, "Cookie": _cookie(host), **(headers or {})}
    body = None
    if form is not None:
        body = urllib.parse.urlencode(form).encode()
        h["Content-Type"] = "application/x-www-form-urlencoded; charset=UTF-8"
    req = urllib.request.Request(url, data=body, method=method, headers=h)
    try:
        r = op.open(req, timeout=timeout)
        _store(host, r.headers)
        return r.status, r.headers, r.read()
    except urllib.error.HTTPError as e:
        _store(host, e.headers)
        return e.code, e.headers, e.read()


def sj(b):
    t = b.decode("utf-8", "replace")
    return json.loads(t.replace("&&&START&&&", "", 1))


SAFE_KEYS = {"code", "result", "desc", "description", "securityStatus", "status", "vCodeLen", "wt", "info", "reason",
             "captchaUrl", "notificationUrl", "tips", "title", "pwd", "child", "miDemo", "registerPwd"}


def shape(o):
    if isinstance(o, dict):
        out = {}
        for k, v in o.items():
            if isinstance(v, (dict, list)):
                out[k] = shape(v)
            elif k in SAFE_KEYS:
                out[k] = v if not (isinstance(v, str) and len(v) > 80) else f"<str len={len(v)}>"
            else:
                out[k] = f"<{type(v).__name__} len={len(str(v))}>"
        return out
    if isinstance(o, list):
        return [shape(x) for x in o[:3]]
    return f"<{type(o).__name__}>"


def save(path, obj):
    json.dump(obj, open(path, "w"))
    os.chmod(path, 0o600)


def post_hdr(extra=None):
    return {"X-Requested-With": "XMLHttpRequest", "Origin": ACCT, "Referer": ACCT + "/fe/service/login/phone", **(extra or {})}


# ---------------- steps ----------------
def bootstrap():
    st, h, b = http("GET", "https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN&ts=1")
    login_url = json.loads(b)["data"]["loginUrl"]
    st, h, b = http("GET", login_url + "&_json=true")
    return sj(b)


def do_send(phone):
    boot = bootstrap()
    device_id = "wb_" + "%08x-%04x-4%03x-8%03x-%012x" % tuple(int.from_bytes(os.urandom(n), "big") for n in (4, 2, 2, 2, 6))
    fp = os.urandom(16).hex()
    jar[("deviceId", "account.xiaomi.com")] = device_id
    jar[("pass_ua", "account.xiaomi.com")] = "web"
    jar[("uLocale", "xiaomi.com")] = "zh_CN"
    # the web client reads the user again in the same way before sending
    eui, enc = encrypt_fields({"user": phone})
    form = {"sid": boot["sid"], "user": enc["user"], "captCode": ""}
    st, h, b = http("POST", ACCT + "/pass/sendServiceLoginTicket?_dc=" + str(int(time.time() * 1000)), form, post_hdr({"eui": eui}))
    r = sj(b)
    print("sendServiceLoginTicket HTTP", st)
    print("response shape:", json.dumps(shape(r), ensure_ascii=False))
    print("Set-Cookie names:", [c.split("=", 1)[0] for c in (h.get_all("Set-Cookie") or [])])
    save(STATE, {"boot": boot, "device_id": device_id, "fp": fp, "phone": phone, "sent_at": time.time(),
                 "jar": {f"{n}@{d}": v for (n, d), v in jar.items()}, "send_response": r})
    ok = r.get("code") == 0
    print("SENT" if ok else "NOT SENT (see code above)")
    return 0 if ok else 1


def do_verify(code):
    s = json.load(open(STATE))
    for k, v in s["jar"].items():
        n, d = k.split("@", 1)
        jar[(n, d)] = v
    boot, phone, fp = s["boot"], s["phone"], s["fp"]
    print(f"state age: {time.time() - s['sent_at']:.0f}s")

    # step 2: pre-check
    form = {"sid": boot["sid"], "user": phone, "ticket": code, "policyName": "miaccount"}
    st, h, b = http("POST", ACCT + "/pass/phoneInfo", form, post_hdr())
    r = sj(b)
    print("\nphoneInfo HTTP", st, "shape:", json.dumps(shape(r), ensure_ascii=False))
    status = (r.get("data") or {}).get("status")
    if r.get("code") != 0 or status != 2:
        print("pre-check did not pass (code=%s, data.status=%s); stopping before login" % (r.get("code"), status))
        return 1

    # step 3: login
    form = {"user": phone, "ticket": code, "_json": "true", "_locale": "zh_CN", "callback": boot["callback"], "sid": boot["sid"],
            "deviceFingerprint": fp, "qs": boot["qs"], "_sign": boot["_sign"], "serviceParam": boot["serviceParam"]}
    st, h, b = http("POST", ACCT + "/pass/serviceLoginTicketAuth", form, post_hdr())
    r = sj(b)
    print("\nserviceLoginTicketAuth HTTP", st, "shape:", json.dumps(shape(r), ensure_ascii=False))
    print("Set-Cookie names:", [c.split("=", 1)[0] for c in (h.get_all("Set-Cookie") or [])])
    if r.get("code") != 0 or not r.get("location"):
        print("login did not return a location; stopping")
        return 2
    if r.get("securityStatus") not in (0, None):
        print("securityStatus != 0: extra verification required:", r.get("securityStatus"))
        return 3

    # follow location
    url = r["location"]
    hops = 0
    while url and hops < 6:
        hops += 1
        st, h, b = http("GET", url, headers={"Referer": ACCT + "/"})
        nxt = h.get("Location")
        print(f" hop {hops}: HTTP {st} host={urllib.parse.urlparse(url).netloc} set-cookie={[c.split('=', 1)[0] for c in (h.get_all('Set-Cookie') or [])]}")
        if not nxt or st not in (301, 302, 303, 307):
            break
        url = urllib.parse.urljoin(url, nxt)

    tok = jar.get(("serviceToken", "i.mi.com"))
    uid = jar.get(("userId", "i.mi.com")) or jar.get(("userId", "mi.com")) or jar.get(("userId", "account.xiaomi.com"))
    pass_token = jar.get(("passToken", "account.xiaomi.com")) or r.get("passToken")
    print("\ngot serviceToken:", bool(tok), "| userId:", bool(uid), "| passToken:", bool(pass_token))
    if tok and uid:
        st, h, b = http("GET", "https://i.mi.com/drive/v2/user/folders/children?parentId=0&pageNo=1&limit=1&type=&order=SERVICE_TIME&reverse=true",
                        headers={"Cookie": f"serviceToken={tok}; userId={uid}"})
        j = json.loads(b)
        print("drive access with the new token: HTTP", st, "code", j.get("code"), "allCount", (j.get("data") or {}).get("allCount"))
    save(RESULT, {"serviceToken": tok, "userId": uid, "passToken": pass_token})
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] not in ("send", "verify"):
        print(__doc__)
        sys.exit(64)
    sys.exit(do_send(sys.argv[2]) if sys.argv[1] == "send" else do_verify(sys.argv[2]))
