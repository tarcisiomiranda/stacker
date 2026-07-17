# Domain specifications

This directory is the canonical inventory of the project's public backend and UI surfaces. Read `index.yaml` first, then open the YAML file for the relevant domain before navigating the source code.

Each domain file follows `_schema.yaml` and lists only items that exist in the repository, always with their real source path. Empty sections are represented by empty lists. The `last_updated` field records the last date on which the domain's public inventory changed.

When adding, removing, or renaming a model, GraphQL operation, REST endpoint, page, hook, component, or public type, update the corresponding domain YAML in the same change. For a new domain, copy `_schema.yaml`, populate it from the implementation, and add it to `index.yaml`. Remove both entries when deleting a domain.
