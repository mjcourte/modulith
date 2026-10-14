import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/_app/shared')({ component: () => <p>Shared</p> })