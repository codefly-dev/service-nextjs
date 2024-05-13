import type { NextApiRequest, NextApiResponse } from 'next';

// Define a type for the response to ensure consistent structure
type ApiResponse = {
    version: string;
    status: string;
};

export default function handler(
    req: NextApiRequest,
    res: NextApiResponse<ApiResponse>
) {
    if (req.method === 'GET') {
        // TODO: From SDK
        // Respond with the version number or other version details
        res.status(200).json({ version: "FIXME", status: "stable" });
    } else {
        // Method Not Allowed
        res.setHeader('Allow', ['GET']);
        res.status(405).end(`Method ${req.method} Not Allowed`);
    }
}
