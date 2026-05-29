require "json"

class ApplicationController
  def params
    { id: "readiness" }
  end

  def render(json:)
    JSON.generate(json)
  end
end
