---
name: Clean Code
description: Write code that is easy to understand and maintain.
---

# Principles

- Optimize for readability.
- Use descriptive names.
- Functions should do one thing.
- Keep files focused.
- Remove duplication.
- Prefer composition over inheritance.
- Avoid unnecessary abstraction.

# Before writing code

Ask:

- Can this be simpler?
- Will another developer understand this in six months?
- Is there an existing implementation?

# During implementation

- Keep functions under ~40 lines when practical.
- Limit nesting.
- Handle errors explicitly.
- Delete dead code instead of commenting it out.

# Review checklist

- [ ] Names are descriptive
- [ ] No duplicated logic
- [ ] Error handling exists
- [ ] Code is formatted
- [ ] Comments explain "why", not "what"