FROM ruby:3.3
WORKDIR /app
COPY Gemfile Gemfile
RUN bundle install || true
COPY . /app
ENV FAULT_MODE=runtime_error
EXPOSE 8080
CMD ["bundle", "exec", "rackup", "-o", "0.0.0.0", "-p", "8080", "config.ru"]
