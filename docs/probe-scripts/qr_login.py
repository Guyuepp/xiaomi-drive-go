"""QR login probe. Prints the QR, long-polls, records the *shape* of the result. Never prints secrets.

Usage: python3 qr_login.py
Writes the captured cookies/tokens to /tmp/mi_auth/qr_result.json (mode 600).
"""
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
OUT = "/tmp/mi_auth/qr_result.json"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


op = urllib.request.build_opener(NoRedirect)
jar = {}   # name -> {domain: value}   (we keep per-host jars manually)


def store_cookies(host, headers):
    for c in headers.get_all("Set-Cookie") or []:
        first = c.split(";", 1)[0]
        if "=" not in first:
            continue
        n, v = first.split("=", 1)
        attrs = {a.split("=", 1)[0].strip().lower(): (a.split("=", 1)[1] if "=" in a else "") for a in c.split(";")[1:]}
        dom = attrs.get("domain", host).lstrip(".")
        if v == "" or "expires" in attrs and "1994" in attrs["expires"]:
            jar.pop((n, dom), None)
        else:
            jar[(n, dom)] = v


def cookie_for(host):
    parts = []
    for (n, dom), v in jar.items():
        if host == dom or host.endswith("." + dom):
            parts.append(f"{n}={v}")
    return "; ".join(parts)


def http(url, headers=None, timeout=30):
    host = urllib.parse.urlparse(url).hostname
    h = {"User-Agent": UA, "Cookie": cookie_for(host), **(headers or {})}
    req = urllib.request.Request(url, headers=h)
    try:
        r = op.open(req, timeout=timeout)
        store_cookies(host, r.headers)
        return r.status, r.headers, r.read()
    except urllib.error.HTTPError as e:
        store_cookies(host, e.headers)
        return e.code, e.headers, e.read()


def sj(b):
    return json.loads(b.decode("utf-8", "replace").replace("&&&START&&&", "", 1))


def shape(o, depth=0):
    """Describe a JSON object without values for sensitive-looking keys."""
    if isinstance(o, dict):
        return {k: (shape(v, depth + 1) if isinstance(v, (dict, list)) else
                    (v if (k in ("code", "result", "desc", "description", "securityStatus", "status", "expire", "timeout")) else f"<{type(v).__name__} len={len(str(v))}>"))
                for k, v in o.items()}
    if isinstance(o, list):
        return [shape(x, depth + 1) for x in o[:3]]
    return f"<{type(o).__name__}>"


st, h, b = http("https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN&ts=1")
login_url = json.loads(b)["data"]["loginUrl"]
st, h, b = http(login_url + "&_json=true")
boot = sj(b)

device_id = "wb_" + "%08x-%04x-4%03x-8%03x-%012x" % tuple(int.from_bytes(os.urandom(n), "big") for n in (4, 2, 2, 2, 6))
fp = os.urandom(16).hex()
jar[("deviceId", "account.xiaomi.com")] = device_id
jar[("pass_ua", "account.xiaomi.com")] = "web"
jar[("uLocale", "xiaomi.com")] = "zh_CN"

q = {"_locale": "zh_CN", "_group": "DEFAULT", "sid": boot["sid"], "qs": boot["qs"], "callback": boot["callback"],
     "_sign": boot["_sign"], "serviceParam": boot["serviceParam"], "showActiveX": "false", "theme": "", "needTheme": "false",
     "bizDeviceType": "", "_hasLogo": "false", "_qrsize": "240", "_dc": str(int(time.time() * 1000)), "deviceFingerprint": fp}
st, h, b = http("https://account.xiaomi.com/longPolling/loginUrl?" + urllib.parse.urlencode(q), {"X-Requested-With": "XMLHttpRequest"})
r = sj(b)
if r.get("code") != 0:
    print("loginUrl failed:", shape(r))
    sys.exit(1)

# QR payload = the 'loginUrl' field? decode the PNG to be sure
qr_png = http(r["qr"])[2]
open("/tmp/mi_auth/qr.png", "wb").write(qr_png)
payload = subprocess.run(["zbarimg", "--raw", "-q", "/tmp/mi_auth/qr.png"], capture_output=True, text=True).stdout.strip()
print("QR payload host:", urllib.parse.urlparse(payload).netloc, "| path:", urllib.parse.urlparse(payload).path)
print("payload == loginUrl field:", payload == r["loginUrl"], "| lp host:", urllib.parse.urlparse(r["lp"]).netloc)
print("\n请用手机「小米账号」App(设置 → 小米账号 → 扫码登录)或米家 App 扫描下面的二维码并确认登录。有效期 %d 秒。\n" % r["timeout"])
subprocess.run(["qrencode", "-t", "ansiutf8", "-m", "1", payload])
sys.stdout.flush()

t0 = time.time()
print("waiting on long-poll ...", flush=True)
try:
    st, h, b = http(r["lp"], {"X-Requested-With": "XMLHttpRequest", "Origin": "https://account.xiaomi.com",
                              "Referer": "https://account.xiaomi.com/"}, timeout=330)
except Exception as e:
    print("long-poll exception:", type(e).__name__, e)
    sys.exit(2)
print(f"long-poll returned after {time.time()-t0:.1f}s, HTTP {st}")
res = sj(b)
print("response shape:", json.dumps(shape(res), ensure_ascii=False))
print("response keys:", sorted(res.keys()))
print("Set-Cookie names on lp response:", [c.split('=', 1)[0] for c in (h.get_all('Set-Cookie') or [])])
json.dump({"lp_response": res, "jar": {f"{n}@{d}": v for (n, d), v in jar.items()}, "device_id": device_id, "fp": fp,
           "boot": {k: boot[k] for k in ("sid", "callback")}}, open(OUT, "w"))
os.chmod(OUT, 0o600)

loc = res.get("location")
if not loc:
    print("no location in response; stop")
    sys.exit(3)

print("\n== following location manually (no auto-redirect)")
hops = 0
url = loc
while url and hops < 8:
    hops += 1
    st, h, b = http(url, {"Referer": "https://account.xiaomi.com/"})
    host = urllib.parse.urlparse(url).netloc
    names = [c.split("=", 1)[0] for c in (h.get_all("Set-Cookie") or [])]
    nxt = h.get("Location")
    print(f" hop {hops}: HTTP {st} host={host} path={urllib.parse.urlparse(url).path} set-cookie={names} next={'<' + urllib.parse.urlparse(nxt).netloc + urllib.parse.urlparse(nxt).path + '>' if nxt else None}")
    if not nxt or st not in (301, 302, 303, 307):
        break
    url = urllib.parse.urljoin(url, nxt)

have = {n: d for (n, d) in jar.keys()}
print("\ncookies after login (names only):", sorted(set(f"{n}@{d}" for (n, d) in jar.keys())))
tok = jar.get(("serviceToken", "i.mi.com")) or jar.get(("serviceToken", "mi.com"))
print("got serviceToken for i.mi.com:", bool(tok), "| len:", len(tok or ""))
json.dump({"lp_response": res, "jar": {f"{n}@{d}": v for (n, d), v in jar.items()}, "device_id": device_id, "fp": fp},
          open(OUT, "w"))
os.chmod(OUT, 0o600)
print("saved to", OUT, "(mode 600)")
