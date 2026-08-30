import json, math, bisect, hashlib
from statistics import NormalDist
src = open('oracle.py').read().split("run('plain40'")[0]

def frame(parts):
    return "".join(f"{len(p)}:{p}" for p in parts).encode()
def numid(v):
    if v == 0: v = 0.0
    r = repr(float(v))
    # Go's strconv 'g' -1 formatting
    from decimal import Decimal
    s = f"{v:.17g}"
    for p in range(1,18):
        c = f"{v:.{p}g}"
        if float(c) == v: return c
    return s

def analyse(datafile, key):
    g = {}
    exec(src.replace("data = json.load(open('candidates.json'))", f"data = json.load(open('{datafile}'))"), g)
    data = g['data']; rank_transform=g['rank_transform']; standardize=g['standardize']
    rows = data[key]; N=len(rows); ids=[r['id'] for r in rows]; strata=[r['stratum'] for r in rows]
    T = rank_transform(standardize(rows)); nfeat=len(T[0]); floor=math.ceil(0.5*nfeat)
    dist=[[None]*N for _ in range(N)]
    for i in range(N):
        for j in range(i+1,N):
            sh=[f for f in range(nfeat) if T[i][f] is not None and T[j][f] is not None]
            if len(sh)>=floor:
                dist[i][j]=dist[j][i]=sum(abs(T[i][f]-T[j][f]) for f in sh)/len(sh)
    k=max(3,min(15,int(math.isqrt(N))))
    dens=[None]*N; nb=[0]*N
    for i in range(N):
        v=sorted(x for x in dist[i] if x is not None); nb[i]=len(v)
        if len(v)>=k: dens[i]=sum(v[:k])/k
    q=[i for i in range(N) if dens[i] is not None]; q.sort(key=lambda i:(dens[i], ids[i]))
    keep=math.ceil(0.75*N); elig=set(q[:keep]) if len(q)>=keep else set(q)
    order=sorted(range(N), key=lambda i: ids[i])
    rowsout=[f"{ids[i]}={numid(dens[i]) if dens[i] is not None else '-'}:{nb[i]}:{'1' if i in elig else '0'}" for i in order]
    print(f"--- {key} N={N} k={k} eligible={len(elig)}")
    print("   density digest:", hashlib.sha256(frame(rowsout)).hexdigest())
    print("   eligible digest:", hashlib.sha256(frame(sorted(ids[i] for i in elig))).hexdigest())
    # eligibility boundary tie?
    if len(q)>=keep and keep < len(q):
        if abs(dens[q[keep-1]]-dens[q[keep]]) < 1e-15:
            print("   ELIGIBILITY BOUNDARY TIE")
        else:
            print("   no eligibility boundary tie")
    by={}
    for i in sorted(elig, key=lambda i: ids[i]): by.setdefault(strata[i],[]).append(i)
    print("   eligible per stratum:", {s:len(v) for s,v in by.items()})

analyse('candidates.json','plain40')
analyse('twin.json','twinstrata')
analyse('candidates.json','mixed')
