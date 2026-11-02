# Cookbook: data-driven feeds

Run a case once per row of data. Each column becomes a `${variable}`.

## CSV feed

`data/users.csv`:
```csv
name,age
ada,36
linus,54
```

```yaml
cases:
  - name: create users
    feed:
      type: csv
      path: data/users.csv     # relative to the project file
    steps:
      - name: create
        protocol: rest
        request:
          method: POST
          url: "${BASE_URL}/users"
          json: { name: "${name}", age: "${age}" }
        assertions:
          - type: status
            value: 201
```

The first CSV row is the header by default. Set `config: { header: false }` to
treat all rows as data (columns become `col0`, `col1`, …).

## JSON feed

`data/users.json`:
```json
[ { "name": "ada", "age": 36 }, { "name": "linus", "age": 54 } ]
```

```yaml
feed:
  type: json
  path: data/users.json
```

## Inline rows (no external file)

```yaml
feed:
  type: csv          # type is required but inline overrides path
  inline:
    - { name: ada, age: "36" }
    - { name: linus, age: "54" }
```

## Reporting

Each iteration appears as a separate case result with its `iteration` index
(also shown as `name[i]` in JUnit), so failures point at the exact row.
