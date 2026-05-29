require "webrick"
require_relative "../app/controllers/application_controller"
require_relative "../app/controllers/orders_controller"

server = WEBrick::HTTPServer.new(
  BindAddress: "0.0.0.0",
  Port: Integer(ENV.fetch("PORT", "8080")),
  AccessLog: [],
  Logger: WEBrick::Log.new($stderr)
)

server.mount_proc "/orders/readiness" do |_request, response|
  begin
    response.status = 200
    response["Content-Type"] = "application/json"
    response.body = OrdersController.new.show
  rescue StandardError => error
    warn error.full_message
    response.status = 503
    response.body = error.message
  end
end

trap("TERM") { server.shutdown }
trap("INT") { server.shutdown }
server.start
