FROM ruby:3.3
WORKDIR /app
COPY Gemfile Gemfile
RUN bundle install || true
COPY . /app
ENV FAULT_MODE=runtime_error
EXPOSE 8080
CMD ["ruby", "config/server.rb"]
