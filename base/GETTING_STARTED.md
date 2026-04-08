# Getting Started

## Development

```bash
cd code
npm install
npm run dev
```

Your app will be running at http://localhost:3000.

## Testing

```bash
npm test
```

## Structure

```
code/
├── src/
│   ├── app/           # App Router pages and layouts
│   ├── lib/           # Shared utilities, hooks, transforms
│   ├── stores/        # Zustand stores
│   └── test/          # Test setup and MSW mocks
├── next.config.ts
├── tsconfig.json
└── package.json
```
