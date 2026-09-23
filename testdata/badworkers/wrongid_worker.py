import json, os, sys
_OUT = os.fdopen(os.dup(sys.stdout.fileno()), "w"); sys.stdout = sys.stderr
_OUT.write(json.dumps({"model":"wrongid","repo":"x","revision":"r","weights_sha256":"s",
    "covariates":False,"quantiles":[0.1,0.5,0.9],"versions":{}})+"\n"); _OUT.flush()
req = json.loads(sys.stdin.readline())
_OUT.write(json.dumps({"id":"999","quantiles":[[[1,2,3]]*int(req["horizon"])]})+"\n"); _OUT.flush()
