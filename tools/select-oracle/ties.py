import json, math, hashlib
from statistics import NormalDist
src = open('oracle.py').read().split("run('plain40'")[0]

def full(datafile, key, n):
    g={}; exec(src.replace("data = json.load(open('candidates.json'))", f"data = json.load(open('{datafile}'))"), g)
    data=g['data']; rows=data[key]; N=len(rows)
    ids=[r['id'] for r in rows]; strata=[r['stratum'] for r in rows]
    T=g['rank_transform'](g['standardize'](rows)); nfeat=len(T[0]); floor=math.ceil(0.5*nfeat)
    dist=[[None]*N for _ in range(N)]
    for i in range(N):
        for j in range(i+1,N):
            sh=[f for f in range(nfeat) if T[i][f] is not None and T[j][f] is not None]
            if len(sh)>=floor: dist[i][j]=dist[j][i]=sum(abs(T[i][f]-T[j][f]) for f in sh)/len(sh)
    k=max(3,min(15,int(math.isqrt(N))))
    dens=[None]*N
    for i in range(N):
        v=sorted(x for x in dist[i] if x is not None)
        if len(v)>=k: dens[i]=sum(v[:k])/k
    q=[i for i in range(N) if dens[i] is not None]; q.sort(key=lambda i:(dens[i], ids[i]))
    keep=math.ceil(0.75*N); elig=set(q[:keep]) if len(q)>=keep else set(q)
    ties=[]
    if len(q)>=keep and keep<len(q) and abs(dens[q[keep-1]]-dens[q[keep]])<1e-12:
        ties.append(f"eligibility:-:{ids[q[keep-1]]}")
    by={}
    for i in sorted(elig, key=lambda i: ids[i]): by.setdefault(strata[i],[]).append(i)
    order=sorted(by, key=lambda s:(-len(by[s]), s))
    sizes=[len(by[s]) for s in order]
    for a in range(len(order)-1):
        if sizes[a]==sizes[a+1]:
            ties.append(f"stratum-order:-:{order[a]}")
            break
    picked=[]; rnd=0
    while len(picked)<n:
        rnd+=1; progress=False
        for s in order:
            if len(picked)>=n: break
            rem=[i for i in by[s] if i not in picked]
            if not rem: continue
            if len(rem)==1: best=rem[0]
            else:
                cand=[]
                for i in rem:
                    ds=[dist[i][j] for j in rem if j!=i and dist[i][j] is not None]
                    if ds: cand.append((sum(ds), ids[i], i))
                if not cand: continue
                cand.sort()
                if len(cand)>1 and abs(cand[0][0]-cand[1][0])<1e-12:
                    ties.append(f"medoid:{rnd}:{cand[0][1]}")
                best=cand[0][2]
            picked.append(best); progress=True
        if not progress: break
    print(f"--- {key} n={n}: ties = {ties}")
    for i in picked: print(f'   "{ids[i]}",')

full('twin.json','twinstrata',2)
full('twinpair.json','twinpair',3)
full('candidates.json','plain40',4)
full('candidates.json','mixed',3)
