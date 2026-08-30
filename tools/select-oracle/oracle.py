import json, math, bisect
from statistics import NormalDist

data = json.load(open('candidates.json'))
ND = NormalDist()
def quantile(u): return ND.inv_cdf(u)   # sqrt2*erfinv(2u-1)

def standardize(rows):
    out = []
    for r in rows:
        vals = []
        for f in r['f']:
            if not f['d'] or not f['svd']:
                vals.append(None); continue
            var = 1.0 + f['sv']
            vals.append(None if var == 0 else (f['v'] - 1.0) / math.sqrt(var))
        out.append(vals)
    return out

def rank_transform(std):
    nfeat = len(std[0]); M = len(std)
    dists = []
    for f in range(nfeat):
        vals = sorted(v[f] for v in std if v[f] is not None)
        dists.append(vals)
    out = []
    for v in std:
        row = []
        for f in range(nfeat):
            if v[f] is None or not dists[f]:
                row.append(None); continue
            d = dists[f]; m = len(d)
            lo = bisect.bisect_left(d, v[f]); hi = bisect.bisect_right(d, v[f])
            u = (lo + (hi - lo) / 2 + 0.5) / (m + 1)
            row.append(quantile(u))
        out.append(row)
    return out

def run(name, n):
    rows = data[name]
    N = len(rows)
    ids = [r['id'] for r in rows]
    strata = [r['stratum'] for r in rows]
    T = rank_transform(standardize(rows))
    nfeat = len(T[0]); floor = math.ceil(0.5 * nfeat)

    dist = [[None]*N for _ in range(N)]
    for i in range(N):
        for j in range(i+1, N):
            shared = [f for f in range(nfeat) if T[i][f] is not None and T[j][f] is not None]
            if len(shared) >= floor:
                d = sum(abs(T[i][f]-T[j][f]) for f in shared)/len(shared)
                dist[i][j] = dist[j][i] = d

    k = max(3, min(15, int(math.isqrt(N))))
    density, nbrs = [None]*N, [0]*N
    for i in range(N):
        valid = sorted(d for d in dist[i] if d is not None)
        nbrs[i] = len(valid)
        if len(valid) >= k:
            density[i] = sum(valid[:k])/k

    qualified = [i for i in range(N) if density[i] is not None]
    qualified.sort(key=lambda i: (density[i], ids[i]))
    keep = math.ceil(0.75*N)
    eligible = set(qualified[:keep]) if len(qualified) >= keep else set(qualified)

    by_stratum = {}
    for i in sorted(eligible, key=lambda i: ids[i]):
        by_stratum.setdefault(strata[i], []).append(i)
    order = sorted(by_stratum, key=lambda s: (-len(by_stratum[s]), s))

    picked, medoids = [], []
    rnd = 0
    while len(picked) < n:
        rnd += 1; progress = False
        for s in order:
            if len(picked) >= n: break
            rem = [i for i in by_stratum[s] if i not in picked]
            if not rem: continue
            if len(rem) == 1:
                best, bestsum = rem[0], 0.0
            else:
                cand = []
                for i in rem:
                    ds = [dist[i][j] for j in rem if j != i and dist[i][j] is not None]
                    if ds: cand.append((sum(ds), ids[i], i))
                if not cand: continue
                cand.sort(); bestsum, _, best = cand[0]
            picked.append(best); medoids.append((rnd, s, ids[best], bestsum)); progress = True
        if not progress: break

    print(f"--- {name} n={n}: N={N} k={k} floor={floor} eligible={len(eligible)} keep={keep} strata={ {s:len(v) for s,v in by_stratum.items()} }")
    if len(picked) < n:
        print("   REFUSE: insufficient eligible"); return
    for r,s,i,sm in medoids:
        print(f"   round {r} stratum {s:<50} sum={sm:.12f}")
    for i in picked:
        print(f'   "{ids[i]}",')

run('plain40', 4)
run('plain40', 3)
run('mixed', 3)

print()
print("=== stratum order discriminator ===")
rows = data['mixed']
pop = {}
for r in rows: pop[r['stratum']] = pop.get(r['stratum'],0)+1
print("by POPULATION count:", sorted(pop, key=lambda s: (-pop[s], s)))
run('mixed', 2)

print()
print("=== more cases ===")
run('mixed', 4)
data['plain30'] = data['plain40'][:30]
run('plain30', 3)

print()
print("=== density extremes, plain40 ===")
rows = data['plain40']; N=len(rows)
T = rank_transform(standardize(rows)); nfeat=len(T[0]); floor=math.ceil(0.5*nfeat)
dist=[[None]*N for _ in range(N)]
for i in range(N):
    for j in range(i+1,N):
        sh=[f for f in range(nfeat) if T[i][f] is not None and T[j][f] is not None]
        if len(sh)>=floor:
            d=sum(abs(T[i][f]-T[j][f]) for f in sh)/len(sh); dist[i][j]=dist[j][i]=d
k=max(3,min(15,int(math.isqrt(N))))
dens=[]
for i in range(N):
    v=sorted(x for x in dist[i] if x is not None)
    dens.append((sum(v[:k])/k, rows[i]['id'], len(v)))
dens.sort()
for d,i,n in dens[:3]: print(f'   densest  {d:.12f}  nbrs={n}  {i}')
for d,i,n in dens[-3:]: print(f'   sparsest {d:.12f}  nbrs={n}  {i}')
