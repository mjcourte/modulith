import { useCreateOrg } from '@/api/orgs'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useState } from 'react'

export function CreateOrgForm() {
    const [name, setName] = useState('')
    const createOrg = useCreateOrg()

    const handleSubmit = (e: React.SubmitEvent) => {// FormEvent deprecated for InputEvent or SubmitEvent
        e.preventDefault()
        createOrg.mutate(name.trim(), { onSuccess: () => setName('') })
    }

    return (
        <form onSubmit={handleSubmit} className="max-w-md space-y-2">
            <div className="flex gap-2">
                <Input value={name} onChange={e => setName(e.target.value)} placeholder="Org name" autoFocus />
                <Button type="submit" disabled={createOrg.isPending || !name.trim()}>Create and Join Organization</Button>
            </div>
            {createOrg.error && <p className="text-sm text-destructive">{createOrg.error.message}</p>}
        </form>
    )
}
