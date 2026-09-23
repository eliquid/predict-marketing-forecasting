import json, os, sys
_OUT = os.fdopen(os.dup(sys.stdout.fileno()), "w"); sys.stdout = sys.stderr
_OUT.write(json.dumps({"model":"garbage","repo":"x","revision":"r","weights_sha256":"s",
    "covariates":False,"quantiles":[0.1,0.5,0.9],"versions":{}})+"\n"); _OUT.flush()
sys.stdin.readline()
_OUT.write("this is not json\n"); _OUT.flush()
