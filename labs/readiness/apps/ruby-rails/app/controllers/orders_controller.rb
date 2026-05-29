class OrdersController < ApplicationController
  def show
    raise "database unavailable" if ENV["FAULT_MODE"] == "runtime_error"

    render json: { status: "BROKEN", order_id: params[:id] }
  end
end
