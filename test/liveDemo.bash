# Test the live proxy server
curl https://localhost:8090/healthz

# Test the live proxy  server with TLS
curl https://localhost:8090/healthz --cacert server.crt

# Test the live proxy server with a request to be proxied
curl -i https://localhost:8090 --cacert server.crt


# Test cache HIT and Max-age header and health check
curl -i https://localhost:8090/api/items --cacert server.crt
curl -i https://localhost:8090/api/items --cacert server.crt


# Test cache HIT and Max-age header and health check
for %i in (items01 items02 items01) do curl -i https://localhost:8090/api/%i --cacert server.crt



# Test cache MISS
curl -i https://localhost:8090/api/items/01 --cacert server.crt
curl -i https://localhost:8090/api/items/02 --cacert server.crt



# Test Error metrics and log
curl -i -X POST https://localhost:8090/api/items -H "Content-Type: application/json" -d '{"id":"01","name":"item01"}' --cacert server.crt


# Test cache BYPASS and load balancear logic 
curl -i -X POST https://localhost:8090/api/items -H "Content-Type: application/json" --data-raw "{\"name\":\"Gamma\",\"value\":30}" --cacert server.crt
curl -i -X POST https://localhost:8090/api/items -H "Content-Type: application/json" --data-raw "{\"name\":\"Delta\",\"value\":40}" --cacert server.crt
curl -i -X POST https://localhost:8090/api/items -H "Content-Type: application/json" --data-raw "{\"name\":\"Epsilon\",\"value\":50}" --cacert server.crt
curl -i -X POST https://localhost:8090/api/items -H "Content-Type: application/json" --data-raw "{\"name\":\"Zeta\",\"value\":60}" --cacert server.crt
curl -i -X POST https://localhost:8090/api/items -H "Content-Type: application/json" --data-raw "{\"name\":\"Eta\",\"value\":70}" --cacert server.crt
curl -i -X POST https://localhost:8090/api/items -H "Content-Type: application/json" --data-raw "{\"name\":\"Theta\",\"value\":80}" --cacert server.crt
curl -i https://localhost:8090/api/items/3 --cacert server.crt
curl -i https://localhost:8090/api/items/3 --cacert server.crt
curl -i https://localhost:8090/api/items/3 --cacert server.crt
curl -i https://localhost:8090/api/items/3 --cacert server.crt
curl -i https://localhost:8090/api/items/3 --cacert server.crt
curl -i https://localhost:8090/api/items/3 --cacert server.crt


# Test cache MISS with diferrent bodies
curl -i -X POST https://localhost:8090/api/cache -H "Content-Type: application/json" --data-raw "{\"test\":\"one\"}" --cacert server.crt
curl -i -X POST https://localhost:8090/api/cache -H "Content-Type: application/json" --data-raw "{\"test\":\"two\"}" --cacert server.crt



# Test high volume
for /L %i in (1,1,200) do curl -i https://localhost:8090/api/items%i --cacert server.crt

