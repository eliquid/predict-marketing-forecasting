# A well-behaved worker: answers every request correctly, forever.
# Used to exercise the multi-request path that the CLI does not currently use.
import json, os, sys
_OUT = os.fdopen(os.dup(sys.stdout.fileno()), "w"); sys.stdout = sys.stderr
_OUT.write(json.dumps({"model":"echo","repo":"x","revision":"r","weights_sha256":"s",
    "covariates":False,"quantiles":[0.1,0.5,0.9],"versions":{}})+"\n"); _OUT.flush()
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    req=json.loads(line)
    h=int(req["horizon"]); n=len(req["quantiles"]); m=len(req["series"])
    _OUT.write(json.dumps({"id":req["id"],
        "quantiles":[[[float(j+i) for j in range(n)] for i in range(h)] for _ in range(m)]})+"\n")
    _OUT.flush()
