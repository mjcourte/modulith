import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/_app/trash')({ component: () => <p>Trash</p> })