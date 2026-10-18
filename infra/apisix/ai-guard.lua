-- Runs in rewrite before ai-proxy's access phase. No prompts or credentials logged.
return function()
    local key = os.getenv("CARTSIDE_AI_SERVICE_KEY")
    local supplied = ngx.req.get_headers()["X-Cartside-AI-Key"]
    if not key or #key < 32 or key:find("^replace_with_") or supplied ~= key then
        return ngx.exit(401)
    end
    ngx.req.clear_header("X-Cartside-AI-Key")
    ngx.req.clear_header("Authorization")
    ngx.req.clear_header("Cookie")
    if ngx.req.get_method() ~= "POST" then return ngx.exit(405) end
    if ngx.var.args then return ngx.exit(400) end
    if ngx.req.get_headers()["Content-Type"] ~= "application/json" then return ngx.exit(415) end
    local core = require("apisix.core")
    local body, err = core.request.get_body(8192)
    if not body then return ngx.exit(413) end
    if #body > 8192 then return ngx.exit(413) end
    local data = core.json.decode(body)
    if type(data) ~= "table" then return ngx.exit(400) end
    for k in pairs(data) do
        if k ~= "model" and k ~= "messages" and k ~= "stream" then return ngx.exit(400) end
    end
    if data.model ~= "cartside-mock-v1" or (data.stream ~= nil and data.stream ~= false) then return ngx.exit(400) end
    if type(data.messages) ~= "table" or #data.messages < 1 or #data.messages > 16 then return ngx.exit(400) end
    for i, m in pairs(data.messages) do
        if type(i) ~= "number" or type(m) ~= "table" then return ngx.exit(400) end
        for k in pairs(m) do if k ~= "role" and k ~= "content" then return ngx.exit(400) end end
        if (m.role ~= "user" and m.role ~= "system" and m.role ~= "assistant") or type(m.content) ~= "string" or not m.content:find("%S") then return ngx.exit(400) end
    end
end
