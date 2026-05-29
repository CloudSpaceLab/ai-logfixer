require_relative "boot"

require "rails"
require "action_controller/railtie"

Bundler.require(*Rails.groups)

module ReadinessLab
  class Application < Rails::Application
    config.load_defaults 7.1
    config.api_only = true
    config.eager_load = false
    config.hosts.clear
    config.secret_key_base = "readiness-lab-only"
    config.consider_all_requests_local = true
    config.logger = Logger.new($stderr)
  end
end
