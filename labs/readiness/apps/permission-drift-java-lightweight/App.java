import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;

public class App {
  public static void main(String[] args) throws Exception {
    HttpServer server = HttpServer.create(new InetSocketAddress("0.0.0.0", 8080), 0);
    server.createContext("/orders/readiness", App::handleReadiness);
    server.start();
  }

  private static void handleReadiness(HttpExchange exchange) throws IOException {
    int status = 200;
    String body = "{\"status\":\"FIXED\",\"lane\":\"permission-drift\"}";
    try {
      Files.readString(Path.of("config", "cert.pem"));
      Files.writeString(Path.of("logs", "audit.log"), "readiness audit\n", StandardOpenOption.APPEND);
    } catch (IOException error) {
      status = 500;
      body = "{\"detail\":\"permission drift: " + escape(error.getMessage()) + "\"}";
    }
    byte[] raw = body.getBytes(StandardCharsets.UTF_8);
    exchange.getResponseHeaders().add("Content-Type", "application/json");
    exchange.sendResponseHeaders(status, raw.length);
    exchange.getResponseBody().write(raw);
    exchange.close();
  }

  private static String escape(String value) {
    return value.replace("\\", "\\\\").replace("\"", "\\\"");
  }
}
