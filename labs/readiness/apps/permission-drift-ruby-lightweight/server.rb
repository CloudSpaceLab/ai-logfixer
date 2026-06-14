require "json"
require "socket"

server = TCPServer.new("0.0.0.0", 8080)

loop do
  client = server.accept
  request_line = client.gets.to_s
  path = request_line.split[1]
  while (line = client.gets)
    break if line == "\r\n"
  end

  status = 200
  payload = {"status" => "FIXED", "lane" => "permission-drift"}
  if path != "/orders/readiness"
    status = 404
    payload = {"error" => "not found"}
  else
    begin
      File.read("public/status.txt")
      File.write("storage/audit.log", "readiness audit\n")
    rescue SystemCallError => error
      status = 500
      payload = {"detail" => "permission drift: #{error.message}"}
    end
  end

  body = JSON.generate(payload)
  reason = {200 => "OK", 404 => "Not Found", 500 => "Internal Server Error"}[status]
  client.write "HTTP/1.1 #{status} #{reason}\r\n"
  client.write "Content-Type: application/json\r\n"
  client.write "Content-Length: #{body.bytesize}\r\n"
  client.write "Connection: close\r\n"
  client.write "\r\n"
  client.write body
  client.close
end
