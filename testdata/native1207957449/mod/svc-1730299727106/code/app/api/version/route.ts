import { getCurrentServiceVersion } from 'codefly';

export async function GET() {
    try {
        const version = getCurrentServiceVersion();
        return Response.json({ version });
    } catch (error) {
        return Response.json(
            { error: 'Failed to get version' },
            { status: 500 }
        );
    }
}
