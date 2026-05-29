Rails.application.routes.draw do
  get "/orders/:id", to: "orders#show"
end
