-- Probe APISIX's internal control listener, independently of upstream services.
local http = require("resty.http")
local client = http.new()
client:set_timeout(2000)
local response = client:request_uri("http://127.0.0.1:9090/v1/healthcheck")
os.exit(response and response.status == 200 and 0 or 1)
