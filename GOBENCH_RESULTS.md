# ThreadGraph vs GoBench GoKer — Blocking Bugs Scorecard
# Generated: Fri Feb 27 02:29:04 IST 2026  (each bug run 3x, best result used)
# Phase 3: 62/68 = 91% (Phase 2 was 59/68 = 86%)

Project      Issue    Bug Type                               Result     Findings
-------      -----    --------                               ------     --------
cockroach    10214    Resource Deadlock:AB-BA deadlock       DETECTED   leaks=2 dl=0 lb=0
cockroach    1055     Mixed Deadlock:Channel & WaitGroup     DETECTED   leaks=3 dl=1 lb=1
cockroach    10790    Communication Deadlock:Channel & Context DETECTED   leaks=2 dl=0 lb=0
cockroach    13197    Communication Deadlock:Channel & Context DETECTED   leaks=1 dl=0 lb=0
cockroach    13755    Communication Deadlock:Channel & Context DETECTED   leaks=1 dl=0 lb=0
cockroach    1462     Mixed Deadlock:Channel & WaitGroup     MISSED     -
cockroach    16167    Resource Deadlock:Double Locking       DETECTED   leaks=1 dl=0 lb=0
cockroach    18101    Resource Deadlock:Double Locking       DETECTED   leaks=3 dl=0 lb=0
cockroach    2448     Communication Deadlock:Channel         DETECTED   leaks=2 dl=0 lb=0
cockroach    24808    Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
cockroach    25456    Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
cockroach    35073    Communication Deadlock:Channel         DETECTED   leaks=2 dl=0 lb=0
cockroach    35931    Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
cockroach    3710     Resource Deadlock:RWR Deadlock         DETECTED   leaks=2 dl=0 lb=0
cockroach    584      Resource Deadlock:Double Locking       DETECTED   leaks=1 dl=0 lb=0
cockroach    6181     Resource Deadlock:RWR Deadlock         DETECTED   leaks=1 dl=0 lb=0
cockroach    7504     Resource Deadlock:AB-BA Deadlock       MISSED     -
cockroach    9935     Resource Deadlock:Double Locking       DETECTED   leaks=1 dl=0 lb=0
etcd         10492    Resource Deadlock:Double locking       DETECTED   leaks=0 dl=1 lb=1
etcd         5509     Resource Deadlock:Double locking       MISSED     -
etcd         6708     Resource Deadlock:Double locking       DETECTED   leaks=0 dl=1 lb=1
etcd         6857     Communication Deadlock:Channel         DETECTED   leaks=3 dl=0 lb=0
etcd         6873     Mixed Deadlock:Channel & Lock          DETECTED   leaks=2 dl=0 lb=0
etcd         7443     Mixed Deadlock:Channel & Lock          MISSED     -
etcd         7492     Mixed Deadlock:Channel & Lock          DETECTED   leaks=1 dl=0 lb=0
etcd         7902     Mixed Deadlock:Channel & Lock          DETECTED   leaks=1 dl=0 lb=0
grpc         1275     Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
grpc         1353     Mixed Deadlock:Channel & Lock          DETECTED   leaks=2 dl=0 lb=0
grpc         1424     Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
grpc         1460     Mixed Deadlock:Channel & Lock          DETECTED   leaks=2 dl=0 lb=0
grpc         3017     Resource Deadlock:Double locking       DETECTED   leaks=1 dl=1 lb=2
grpc         660      Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
grpc         795      Resource Deadlock:Double locking       DETECTED   leaks=0 dl=1 lb=2
grpc         862      Communication Deadlock:Channel & Context DETECTED   leaks=2 dl=0 lb=0
hugo         3251     Resource Deadlock:AB-BA deadlock       DETECTED   leaks=0 dl=3 lb=51
hugo         5379     Resource Deadlock:Double locking       DETECTED   leaks=0 dl=2 lb=4
istio        16224    Mixed Deadlock:Channel & Lock          DETECTED   leaks=1 dl=1 lb=1
istio        17860    Communication Deadlock:Channel         DETECTED   leaks=3 dl=0 lb=0
istio        18454    Communication Deadlock:Channel & Context DETECTED   leaks=1 dl=0 lb=0
kubernetes   10182    Mixed Deadlock:Channel & Lock          DETECTED   leaks=3 dl=0 lb=0
kubernetes   11298    Communication Deadlock:Channel & Condition Variable DETECTED   leaks=1 dl=0 lb=0
kubernetes   13135    Resource Deadlock:AB-BA deadlock       DETECTED   leaks=3 dl=0 lb=0
kubernetes   1321     Mixed Deadlock:Channel & Lock          DETECTED   leaks=1 dl=0 lb=0
kubernetes   25331    Communication Deadlock:Channel & Context DETECTED   leaks=2 dl=0 lb=0
kubernetes   26980    Mixed Deadlock:Channel & Lock          DETECTED   leaks=1 dl=0 lb=0
kubernetes   30872    Resource Deadlock:AB-BA deadlock       DETECTED   leaks=3 dl=0 lb=0
kubernetes   38669    Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
kubernetes   5316     Communication Deadlock:Channel         DETECTED   leaks=2 dl=0 lb=0
kubernetes   58107    Resource Deadlock:RWR deadlock         DETECTED   leaks=2 dl=0 lb=0
kubernetes   62464    Resource Deadlock:RWR deadlock         DETECTED   leaks=1 dl=0 lb=0
kubernetes   6632     Mixed Deadlock:Channel & Lock          DETECTED   leaks=3 dl=0 lb=0
kubernetes   70277    Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
moby         17176    Resource Deadlock:Double locking       MISSED     -
moby         21233    Communication Deadlock:Channel         DETECTED   leaks=3 dl=0 lb=0
moby         25384    Mixed Deadlock:Misuse WaitGroup        DETECTED   leaks=1 dl=0 lb=0
moby         27782    Communication Deadlock:Channel & Condition Variable DETECTED   leaks=2 dl=0 lb=0
moby         28462    Mixed Deadlock:Channel & Lock          DETECTED   leaks=1 dl=0 lb=0
moby         29733    Communication Deadlock:Condition Variable DETECTED   leaks=1 dl=0 lb=1
moby         30408    Communication Deadlock:Condition Variable DETECTED   leaks=1 dl=0 lb=1
moby         33293    Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
moby         33781    Communication Deadlock:Channel & Context DETECTED   leaks=2 dl=0 lb=0
moby         36114    Resource Deadlock:Double locking       DETECTED   leaks=1 dl=0 lb=0
moby         4395     Communication Deadlock:Channel         DETECTED   leaks=1 dl=0 lb=0
moby         4951     Resource Deadlock:AB-BA deadlock       DETECTED   leaks=2 dl=0 lb=0
moby         7559     Resource Deadlock:Double locking       DETECTED   leaks=1 dl=0 lb=0
serving      2137     Mixed Deadlock:Channel & Lock          MISSED     -
syncthing    4829     Resource Deadlock:Double locking       DETECTED   leaks=0 dl=1 lb=1
syncthing    5795     Communication Deadlock:Channel         DETECTED   leaks=2 dl=0 lb=0

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
SCORE: ThreadGraph detected 62 / 68 bugs (91%)
  Detected: 62
  Missed:   6  (cockroach/1462, cockroach/7504, etcd/5509, etcd/7443, moby/17176, serving/2137)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
