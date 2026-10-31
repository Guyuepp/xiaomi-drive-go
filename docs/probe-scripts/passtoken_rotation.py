"""Does serviceLogin rotate passToken, and does the old one keep working? Never prints secrets."""
import hashlib
import json
import urllib.error
import urllib.parse
import urllib.request

UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
saved = json.load(open("/tmp/mi_auth/qr_result.json"))
J = {tuple(k.split("@", 1)): v for k, v in saved["jar"].items()}
P0 = J[("passToken", "account.xiaomi.com")]
UID = J[("userId", "account.xiaomi.com")]


def fp(x):
    return hashlib.sha256(x.encode()).hexdigest()[:8]


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


op = urllib.request.build_opener(NoRedirect)


def http(url, cookie=""):
    req = urllib.request.Request(url, headers={"User-Agent": UA, "Cookie": cookie})
    try:
        r = op.open(req, timeout=30)
        return r.status, r.headers, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers, e.read()


def cookies(h):
    out = {}
    for c in h.get_all("Set-Cookie") or []:
        first, *attrs = c.split(";")
        if "=" in first:
            n, v = first.split("=", 1)
            if v:
                out[n.strip()] = (v, [a.strip() for a in attrs])
    return out


def sj(b):
    return json.loads(b.decode("utf-8", "replace").replace("&&&START&&&", "", 1))


def login_url():
    return json.loads(http("https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN&ts=1")[2])["data"]["loginUrl"]


def refresh(pass_token):
    st, h, b = http(login_url() + "&_json=true", f"passToken={pass_token}; userId={UID}")
    r = sj(b)
    c = cookies(h)
    newp = c.get("passToken")
    exp = None
    if newp:
        for a in newp[1]:
            if a.lower().startswith("expires="):
                exp = a.split("=", 1)[1]
    body_tok = r.get("passToken")
    return r, newp[0] if newp else None, exp, body_tok


print("P0 fingerprint:", fp(P0))
r1, p1, exp1, b1 = refresh(P0)
print("refresh #1 using P0: code", r1.get("code"), "| Set-Cookie passToken:", bool(p1), "| same as P0:", p1 == P0, "| new fp:", fp(p1) if p1 else None, "| expires:", exp1, "| body passToken present:", bool(b1))
r2, p2, exp2, b2 = refresh(P0)
print("refresh #2 using P0 again (old token): code", r2.get("code"), "| works:", r2.get("code") == 0 and bool(r2.get("location")), "| new passToken same as #1:", p2 == p1)
if p1 and p1 != P0:
    r3, p3, exp3, b3 = refresh(p1)
    print("refresh #3 using P1 (rotated): code", r3.get("code"), "| works:", r3.get("code") == 0 and bool(r3.get("location")), "| P2 differs:", p3 != p1, "| expires:", exp3)
    r4, p4, _, _ = refresh(P0)
    print("refresh #4 using P0 after P1 was used: works:", r4.get("code") == 0 and bool(r4.get("location")))
