"""Can a long-lived passToken alone re-issue an i.mi.com serviceToken? Never prints secrets."""
import json
import urllib.error
import urllib.parse
import urllib.request

UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
saved = json.load(open("/tmp/mi_auth/qr_result.json"))
J = {tuple(k.split("@", 1)): v for k, v in saved["jar"].items()}
PASS = J[("passToken", "account.xiaomi.com")]
UID = J[("userId", "account.xiaomi.com")]
CUID = J.get(("cUserId", "account.xiaomi.com"))
DEV = saved["device_id"]


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


op = urllib.request.build_opener(NoRedirect)


def http(url, cookie="", headers=None):
    req = urllib.request.Request(url, headers={"User-Agent": UA, "Cookie": cookie, **(headers or {})})
    try:
        r = op.open(req, timeout=30)
        return r.status, r.headers, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers, e.read()


def sj(b):
    return json.loads(b.decode("utf-8", "replace").replace("&&&START&&&", "", 1))


def set_cookies(h):
    out = {}
    for c in h.get_all("Set-Cookie") or []:
        first = c.split(";", 1)[0]
        if "=" in first:
            n, v = first.split("=", 1)
            if v:
                out[n] = v
    return out


def drive_ok(service_token, uid):
    st, h, b = http("https://i.mi.com/drive/v2/user/folders/children?parentId=0&pageNo=1&limit=1&type=&order=SERVICE_TIME&reverse=true",
                    f"serviceToken={service_token}; userId={uid}")
    try:
        j = json.loads(b)
        return st, j.get("code"), (j.get("data") or {}).get("allCount")
    except Exception:
        return st, None, None


def refresh(label, cookie):
    st, h, b = http("https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN&ts=1")
    login_url = json.loads(b)["data"]["loginUrl"]
    st, h, b = http(login_url + "&_json=true", cookie)
    r = sj(b)
    keys = sorted(r.keys())
    loc = r.get("location")
    print(f"[{label}] serviceLogin HTTP {st} code={r.get('code')} result={r.get('result')} securityStatus={r.get('securityStatus')} has_location={bool(loc)}")
    print(f"      keys={keys}")
    print(f"      Set-Cookie names={sorted(set_cookies(h).keys())}")
    if not loc:
        return None
    st, h, b = http(loc, cookie)
    sc = set_cookies(h)
    print(f"      sts HTTP {st} next={urllib.parse.urlparse(h.get('Location') or '').path or None} set-cookie={sorted(sc.keys())}")
    tok = sc.get("serviceToken")
    if tok:
        print(f"      new serviceToken len={len(tok)} drive check (status, code, allCount) = {drive_ok(tok, UID)}")
    return tok


print("== A) passToken + userId + deviceId (what a persisted session would hold)")
tA = refresh("A", f"passToken={PASS}; userId={UID}; deviceId={DEV}; pass_ua=web")
print("\n== B) passToken + userId only")
tB = refresh("B", f"passToken={PASS}; userId={UID}")
print("\n== C) passToken only")
tC = refresh("C", f"passToken={PASS}")
print("\n== D) passToken + userId + cUserId + deviceId")
tD = refresh("D", f"passToken={PASS}; userId={UID}; cUserId={CUID}; deviceId={DEV}; pass_ua=web")
print("\n== E) wrong passToken (control)")
refresh("E", f"passToken={'x' * 40}; userId={UID}; deviceId={DEV}")
print("\n== F) no cookies (control)")
refresh("F", "")

print("\n== is the original serviceToken from the QR login still valid? (it is <15 min old)")
orig = J.get(("serviceToken", "i.mi.com"))
print("original token drive check:", drive_ok(orig, UID) if orig else None)
print("two refreshed tokens differ from each other:", bool(tA and tB and tA != tB), "| differ from original:", bool(tA and tA != orig))
json.dump({"serviceToken": tA or tB or tD, "userId": UID}, open("/tmp/mi_auth/refreshed.json", "w"))
import os
os.chmod("/tmp/mi_auth/refreshed.json", 0o600)
