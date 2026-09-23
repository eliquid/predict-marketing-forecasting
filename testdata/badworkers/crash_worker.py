import json, os, sys
_OUT = os.fdopen(os.dup(sys.stdout.fileno()), "w"); sys.stdout = sys.stderr
_OUT.write(json.dumps({"model":"crash","repo":"x","revision":"r","weights_sha256":"s",
    "covariates":False,"quantiles":[0.1,0.5,0.9],"versions":{}})+"\n"); _OUT.flush()
sys.stdin.readline()
sys.exit(3)                       # die mid-request
