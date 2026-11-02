# Cookbook: property transfer (chaining steps)

Capture a value from one step's response and use it in a later step.

```yaml
schemaVersion: "1.0"
name: chaining
environments:
  - name: local
    default: true
    variables: { BASE_URL: "${env:BASE_URL}" }
suites:
  - name: Orders
    cases:
      - name: create then fetch
        steps:
          - name: create order
            protocol: rest
            request:
              method: POST
              url: "${BASE_URL}/orders"
              json: { item: "widget", qty: 2 }
            assertions:
              - type: status
                value: 201
            transfers:
              - name: orderId        # capture into a variable
                from: jsonpath
                expr: "$.id"
          - name: fetch order
            protocol: rest
            request:
              method: GET
              url: "${BASE_URL}/orders/${orderId}"   # use the captured value
            assertions:
              - type: status
                value: 200
              - type: jsonpath
                expr: "$.id"
                value: "${orderId}"
```

## Transfer sources (`from`)

| from       | expr            | captures |
|------------|-----------------|----------|
| `jsonpath` | a JSONPath      | matched JSON value |
| `xpath`    | an XPath        | node inner text |
| `header`   | header name     | response header value |
| `status`   | —               | numeric status code |
| `body`     | —               | the whole raw body |
| `regex`    | a pattern       | first capture group (or whole match) |

Use `default:` to provide a fallback when extraction finds nothing. Transfers
run after assertions; a transfer that errors with no default is reported as a
failed synthetic assertion. When the step sets `retry`, transfers run only on
the final attempt.
